#!/usr/bin/env python3
"""
bench_ray.py – six-metric CVM Ray worker benchmark.

Metrics
-------
  1  enrollment_ms  – ray.init() → worker node appears in ray.nodes()
  2  t_round_ms     – remote_fn.remote() → ray.get() wall time
  3  t_compute_ms   – worker-side compute only (self-reported via perf_counter)
  4  t_scatter_ms   – ray.put() of task inputs on the head
  5  t_gather_ms    – t_round − t_compute  (queue + object fetch + return path)
  6  throughput     – GFLOPS for DGEMM, Mop/s for EP and CG

Workloads
---------
  DGEMM   N = 512, 1024, 2048          2·N³ FLOPs
  EP      pairs = 2²⁴, 2²⁶, 2²⁸       1 op/pair
  CG      Class S (N=1 400)
          Class W (N=7 000)
          Class A (N=14 000)            2·nnz·iters ops

Speedup / efficiency (metric from the spec)
-------------------------------------------
  Requires multiple worker nodes. Run with --workers N after starting N CVMs.
  S(N) = T(1) / T(N),   E(N) = S(N) / N
  When --workers 1 (default) this section is skipped.

Usage
-----
  # Start Ray head on host
  ray start --head --port=6379

  # Boot one or more CVMs, then:
  python bench_ray.py [--repeat N] [--timeout SEC] [--workers N] [--csv FILE]
"""

import argparse
import csv
import statistics
import sys
import time
from dataclasses import dataclass, fields

import numpy as np
import ray


# ---------------------------------------------------------------------------
# NAS benchmark parameters
# ---------------------------------------------------------------------------

CG_CLASSES = {
    "S": dict(n=1_400,  nz=7,  niter=15, shift=10),
    "W": dict(n=7_000,  nz=8,  niter=15, shift=12),
    "A": dict(n=14_000, nz=11, niter=15, shift=20),
}
DGEMM_SIZES = [512, 1024, 2048]
EP_PAIRS    = [1 << 24, 1 << 26, 1 << 28]
EP_BATCH    = 1 << 22   # 4 M pairs per chunk to cap peak memory at ~64 MB


# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------

@dataclass
class Result:
    workload:      str
    size:          str
    t_scatter_ms:  float
    t_round_ms:    float
    t_compute_ms:  float
    t_gather_ms:   float
    throughput:    float
    unit:          str   # "GFLOPS" or "Mop/s"


# ---------------------------------------------------------------------------
# Remote workloads
# ---------------------------------------------------------------------------

@ray.remote
def remote_dgemm(A, B):
    """C = A @ B.  Returns (t_compute_s, flops)."""
    import time
    import numpy as np
    t0 = time.perf_counter()
    C = np.matmul(A, B)
    t = time.perf_counter() - t0
    _ = float(C[0, 0])          # prevent dead-code elimination
    return t, 2 * A.shape[0] ** 3


@ray.remote
def remote_ep(num_pairs, batch_size):
    """
    NAS EP: count Gaussian pairs inside the unit circle.
    Generates in batches to cap peak memory.
    Returns (t_compute_s, num_pairs, inside_count).
    """
    import time
    import numpy as np
    inside = 0
    remaining = num_pairs
    t0 = time.perf_counter()
    while remaining > 0:
        n = min(batch_size, remaining)
        xy = np.random.standard_normal((n, 2))
        inside += int(np.sum(xy[:, 0] ** 2 + xy[:, 1] ** 2 < 1.0))
        remaining -= n
    t = time.perf_counter() - t0
    return t, num_pairs, inside


@ray.remote
def remote_cg(n, nz, niter, shift, b):
    """
    NAS CG: conjugate gradient on a sparse SPD matrix.
    Returns (t_compute_s, ops, converged).
    """
    import time
    import numpy as np
    import scipy.sparse as sp
    import scipy.sparse.linalg as spla

    rng = np.random.default_rng(0)
    cols = rng.integers(0, n, size=(n, nz))
    ones = np.ones(n * nz, dtype=np.float64)
    rows = np.repeat(np.arange(n), nz)
    A = sp.coo_matrix((ones, (rows, cols.ravel())), shape=(n, n)).tocsr()
    A = A + A.T
    diag = np.asarray(A.sum(axis=1)).ravel() + shift
    A = (A + sp.diags(diag)).tocsr()

    t0 = time.perf_counter()
    _x, info = spla.cg(A, b, maxiter=niter, rtol=1e-8)
    t = time.perf_counter() - t0

    return t, 2 * A.nnz * niter, info == 0


# ---------------------------------------------------------------------------
# Per-run measurement helpers
# ---------------------------------------------------------------------------

def _once_dgemm(n):
    A = np.random.standard_normal((n, n))
    B = np.random.standard_normal((n, n))

    t0 = time.perf_counter()
    A_ref = ray.put(A)
    B_ref = ray.put(B)
    t_scatter = time.perf_counter() - t0

    t1 = time.perf_counter()
    t_compute, flops = ray.get(remote_dgemm.remote(A_ref, B_ref))
    t_round = time.perf_counter() - t1

    return t_scatter, t_round, t_compute, flops


def _once_ep(num_pairs):
    # Scatter for EP is trivially small (int argument); measure it anyway
    # to establish the Ray overhead floor.
    dummy = np.array([num_pairs], dtype=np.int64)
    t0 = time.perf_counter()
    _ref = ray.put(dummy)
    t_scatter = time.perf_counter() - t0

    t1 = time.perf_counter()
    t_compute, ops, _count = ray.get(remote_ep.remote(num_pairs, EP_BATCH))
    t_round = time.perf_counter() - t1

    return t_scatter, t_round, t_compute, ops


def _once_cg(n, nz, niter, shift):
    b = np.ones(n, dtype=np.float64)

    t0 = time.perf_counter()
    b_ref = ray.put(b)
    t_scatter = time.perf_counter() - t0

    t1 = time.perf_counter()
    t_compute, ops, converged = ray.get(remote_cg.remote(n, nz, niter, shift, b_ref))
    t_round = time.perf_counter() - t1

    if not converged:
        print(f"  [warn] CG n={n} did not converge", file=sys.stderr)

    return t_scatter, t_round, t_compute, ops


# ---------------------------------------------------------------------------
# Benchmark runner (repeats → median)
# ---------------------------------------------------------------------------

def bench(workload, size, runner, repeat, unit):
    samples = []
    for i in range(repeat):
        print(f"  {workload:5s} {size:10s}  run {i+1}/{repeat}", end="\r", flush=True)
        samples.append(runner())
    print()

    med = lambda idx: statistics.median(s[idx] for s in samples)
    t_scatter = med(0)
    t_round   = med(1)
    t_compute = med(2)
    ops       = samples[0][3]

    t_gather   = max(0.0, t_round - t_compute)
    scale      = 1e9 if unit == "GFLOPS" else 1e6
    throughput = ops / t_compute / scale

    return Result(
        workload=workload,
        size=size,
        t_scatter_ms=t_scatter * 1e3,
        t_round_ms=t_round   * 1e3,
        t_compute_ms=t_compute * 1e3,
        t_gather_ms=t_gather  * 1e3,
        throughput=throughput,
        unit=unit,
    )


# ---------------------------------------------------------------------------
# Enrollment (metric 1)
# ---------------------------------------------------------------------------

def wait_for_workers(n_workers, timeout, head_ip):
    t0 = time.perf_counter()
    print(f"Waiting for {n_workers} CVM worker(s)...", end="", flush=True)
    while True:
        alive = [nd for nd in ray.nodes()
                 if nd["Alive"] and nd["NodeManagerAddress"] != head_ip]
        if len(alive) >= n_workers:
            elapsed = time.perf_counter() - t0
            addrs = ", ".join(nd["NodeManagerAddress"] for nd in alive[:n_workers])
            print(f" connected [{addrs}].")
            return elapsed * 1e3
        if time.perf_counter() - t0 > timeout:
            print()
            sys.exit(f"Timed out waiting for {n_workers} worker(s).")
        time.sleep(0.5)


# ---------------------------------------------------------------------------
# Speedup / efficiency (metric 5)
# ---------------------------------------------------------------------------

def measure_speedup(n_workers, repeat):
    """
    Run DGEMM N=1024 with 1 worker (serial) and then with all N workers
    (parallel) to compute S(N) and E(N).
    """
    if n_workers < 2:
        return None

    SIZE = 1024

    def serial():
        A = np.random.standard_normal((SIZE, SIZE))
        B = np.random.standard_normal((SIZE, SIZE))
        A_ref = ray.put(A)
        B_ref = ray.put(B)
        t0 = time.perf_counter()
        ray.get(remote_dgemm.remote(A_ref, B_ref))
        return time.perf_counter() - t0

    def parallel():
        refs = []
        for _ in range(n_workers):
            A = np.random.standard_normal((SIZE, SIZE))
            B = np.random.standard_normal((SIZE, SIZE))
            refs.append(remote_dgemm.remote(ray.put(A), ray.put(B)))
        t0 = time.perf_counter()
        ray.get(refs)
        return time.perf_counter() - t0

    t1 = statistics.median(serial() for _ in range(repeat))
    tN = statistics.median(parallel() for _ in range(repeat))

    speedup    = t1 / tN
    efficiency = speedup / n_workers
    return t1 * 1e3, tN * 1e3, speedup, efficiency


# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

HEADER = (
    f"{'Workload':<8} {'Size':<10} "
    f"{'t_scatter':>11} {'t_round':>9} {'t_compute':>11} {'t_gather':>9} "
    f"{'Throughput':>12}  {'Unit'}"
)
SEP = "─" * len(HEADER)

def _fmt(ms):
    return f"{ms:8.1f} ms"

def print_results(enrollment_ms, results, speedup_row, n_workers):
    print()
    print(f"  ┌─ Metric 1 – Enrollment: {enrollment_ms:.0f} ms")
    print()
    print("  " + HEADER)
    print("  " + SEP)
    for r in results:
        print(
            f"  {r.workload:<8} {r.size:<10} "
            f"  {_fmt(r.t_scatter_ms)} {_fmt(r.t_round_ms)} "
            f"  {_fmt(r.t_compute_ms)} {_fmt(r.t_gather_ms)} "
            f"  {r.throughput:10.3f}   {r.unit}"
        )
    print()
    if speedup_row:
        t1, tN, S, E = speedup_row
        print(
            f"  ┌─ Metric 5 – Speedup / efficiency  (DGEMM N=1024, {n_workers} workers)\n"
            f"  │  T(1)={t1:.1f} ms   T({n_workers})={tN:.1f} ms\n"
            f"  │  S({n_workers}) = {S:.2f}   E({n_workers}) = {E:.2f}"
        )
        print()
    print(
        "  Notes:\n"
        "    t_scatter  = ray.put() of task inputs on the head\n"
        "    t_round    = remote_fn.remote() → ray.get() (excludes scatter)\n"
        "    t_compute  = worker self-reported (perf_counter around core compute)\n"
        "    t_gather   = t_round − t_compute  (queue + object fetch + return path)\n"
        "    Throughput = ops / t_compute"
    )


def _mpi_size(workload, size):
    """Normalise size label to match MPI summary format."""
    if workload == "DGEMM":
        return size.removeprefix("N=")
    if workload == "CG":
        return size.removeprefix("Class ")
    return size  # EP: "2^24" etc. unchanged


def _mpi_unit(unit):
    return "GFLOP/s" if unit == "GFLOPS" else unit


def write_csv(path, enrollment_ms, results, speedup_row, n_workers):
    # speedup_row covers only DGEMM N=1024; build a lookup for that one entry.
    speedup_lookup = {}
    if speedup_row:
        _t1, _tN, S, E = speedup_row
        speedup_lookup[("DGEMM", "1024")] = (S, E * 100)

    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow([
            "workload", "size", "N", "enroll_ms",
            "scatter_ms", "compute_ms", "gather_ms", "scat+gath_ms",
            "round_ms", "speedup", "effic_%", "throughput", "unit",
        ])
        for r in results:
            size = _mpi_size(r.workload, r.size)
            scat_gath = r.t_scatter_ms + r.t_gather_ms
            key = (r.workload, size)
            if n_workers == 1:
                speedup_val, effic_val = "1.000", "100.000"
            elif key in speedup_lookup:
                S, E = speedup_lookup[key]
                speedup_val, effic_val = f"{S:.3f}", f"{E:.3f}"
            else:
                speedup_val, effic_val = "", ""
            w.writerow([
                r.workload,
                size,
                n_workers,
                f"{enrollment_ms:.3f}",
                f"{r.t_scatter_ms:.3f}",
                f"{r.t_compute_ms:.3f}",
                f"{r.t_gather_ms:.3f}",
                f"{scat_gath:.3f}",
                f"{r.t_round_ms:.3f}",
                speedup_val,
                effic_val,
                f"{r.throughput:.3f}",
                _mpi_unit(r.unit),
            ])
    print(f"  CSV written to {path}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--repeat",   type=int, default=3,
                    help="timed repetitions per configuration (default: 3)")
    ap.add_argument("--timeout",  type=int, default=180,
                    help="seconds to wait for workers (default: 180)")
    ap.add_argument("--workers",  type=int, default=1,
                    help="number of CVM workers expected (default: 1)")
    ap.add_argument("--skip-cg-a", action="store_true",
                    help="skip CG Class A (N=14000, can be slow)")
    ap.add_argument("--csv",      metavar="FILE",
                    help="also write results to a CSV file")
    args = ap.parse_args()

    import warnings
    warnings.filterwarnings("ignore")          # suppress Ray FutureWarning noise

    ray.init(address="auto")

    head_ip = ray.get_runtime_context().gcs_address.split(":")[0]

    # Metric 1: enrollment
    existing = [nd for nd in ray.nodes()
                if nd["Alive"] and nd["NodeManagerAddress"] != head_ip]
    if len(existing) >= args.workers:
        addrs = ", ".join(nd["NodeManagerAddress"] for nd in existing[:args.workers])
        print(f"Worker(s) already connected [{addrs}]; enrollment time not captured.")
        enrollment_ms = 0.0
    else:
        enrollment_ms = wait_for_workers(args.workers, args.timeout, head_ip)

    results = []

    # DGEMM – metrics 2-6
    for n in DGEMM_SIZES:
        results.append(bench("DGEMM", f"N={n}",
                             lambda n=n: _once_dgemm(n),
                             args.repeat, "GFLOPS"))

    # EP – metrics 2-6
    for pairs in EP_PAIRS:
        exp = pairs.bit_length() - 1
        results.append(bench("EP", f"2^{exp}",
                             lambda p=pairs: _once_ep(p),
                             args.repeat, "Mop/s"))

    # CG – metrics 2-6
    cg_classes = ["S", "W"] + ([] if args.skip_cg_a else ["A"])
    for cls in cg_classes:
        p = CG_CLASSES[cls]
        results.append(bench("CG", f"Class {cls}",
                             lambda p=p: _once_cg(**p),
                             args.repeat, "Mop/s"))

    # Metric 5: speedup / efficiency (only meaningful with >1 worker)
    speedup_row = measure_speedup(args.workers, args.repeat)

    print_results(enrollment_ms, results, speedup_row, args.workers)

    if args.csv:
        write_csv(args.csv, enrollment_ms, results, speedup_row, args.workers)

    ray.shutdown()


if __name__ == "__main__":
    main()
