# Fort

Fort is a small ATLS/Exported Authenticator demo for a guest client and a
host verifier, extended with a Ray distributed computing layer running inside
SEV-SNP Confidential VMs (CVMs).

## Roles

- `fort/client` is the Attester. It runs in the Buildroot guest, connects to
  the verifier, answers the server's Exported Authenticator request with client
  attestation material, and then sends `Hello World!`.
- `fort/server` is the verifier. It listens for the guest, sends the
  `CertificateRequest`-style Exported Authenticator request with the
  `cmw_attestation` offer, verifies the client's authenticator and attestation
  binding, and prints the received message to stdout.

## Buildroot Image

The external Buildroot tree in `buildroot/` enables:

- `BR2_PACKAGE_FORT_CLIENT=y`, which builds and installs `/usr/bin/client`.
- `fort-client.service`, which starts the ATLS client on boot.
- `fort-init.service`, which configures networking and starts the Ray worker.
- `board/fort/post-image.sh`, which generates `output/images/start-qemu.sh`.

Build the kernel and initramfs with:

```sh
git clone https://github.com/buildroot/buildroot.git
git clone https://github.com/danko-miladinovic/fort.git

cd buildroot

make BR2_EXTERNAL=../fort/buildroot fort_defconfig
make
```

The image uses Python 3.14.4, which must match the Python version in the Ray
head's virtual environment on the host. Ray enforces an exact Python version
match between head and worker nodes.

## Run The Verifier

Start the server on the host before booting the guest:

```sh
cd fort/server
ATLS_ADDR=0.0.0.0:9443 go run .
```

After the client connects, the server prints:

```text
Hello World!
```

## Boot The Client Guest

After Buildroot finishes, run the generated QEMU launcher:

```sh
cd buildroot
output/images/start-qemu.sh
```

Inside the guest, `fort/client` resolves the verifier address in this order:

1. `ATLS_ADDR` environment variable
2. `VERIFIER_IP` and `VERIFIER_PORT` environment variables
3. kernel command line `verifier_ip` and `verifier_port`
4. `127.0.0.1:9443`

Whether the client uses real SEV-SNP attestation or dummy attestation evidence
is controlled by the kernel command line parameter `atls_snp_attestation`:
- `atls_snp_attestation=true` — real SEV-SNP (used by `launch.sh`)
- `atls_snp_attestation=false` — dummy attestation (used by `launch-nosnp.sh`)

This lets the same Buildroot image run in both SEV and plain VMs without
rebuilding.

## Ray Worker in the CVM

The guest image runs a Ray worker that joins a head node on the host.
On boot, `fort-init.service` runs `/root/network_up.sh` (static IP setup) and
then `/root/ray_init.sh` (starts the pre-installed Ray worker).

### Networking — Linux Bridge

CVMs use a Linux bridge (`br0`) for networking. Each worker gets a static IP
on the `192.168.100.0/24` subnet:

| Component | IP |
|---|---|
| Host bridge (`br0`) | `192.168.100.1` |
| Worker 1 | `192.168.100.2` |
| Worker 2 | `192.168.100.3` |
| … | … |
| Worker N | `192.168.100.(N+1)` |

Each CVM connects via its own tap device (`tap0`, `tap1`, …). The bridge gives
the head direct IP access to each worker, so no port forwarding is needed.

### 1. Set Up the Bridge

Run once after each host reboot:

```sh
# For N workers, creates tap0..tap(N-1)
sudo fort/setup-bridge.sh 14
```

Verify the bridge is up:

```sh
ip addr show br0
bridge link show
```

### 2. Start the Verifier (ATLS Server)

The verifier performs SEV-SNP attestation and acts as a CA. On startup it
generates a CA key, writes `ca.crt`, `head.crt`, and `head.key` to the
working directory, then waits for CVM connections.

```sh
cd fort/server
go build -o server .
```

**With real AMD hardware** (full attestation + measurement enforcement):

```sh
# Compute the expected measurement first (see compute-measurement below)
MEASUREMENT=$(../tools/compute-measurement/compute-measurement \
    -kernel /path/to/bzImage \
    -initrd /path/to/rootfs.cpio.gz \
    -append "console=ttyS0 root=/dev/ram0 rw verifier_ip=192.168.100.1 verifier_port=9443 atls_snp_attestation=true")

RAY_HEAD_IP=147.91.12.238 FORT_EXPECTED_MEASUREMENT="$MEASUREMENT" ./server
```

**Without AMD hardware** (skip AMD VCEK chain check; accept dummy evidence from plain VMs):

```sh
RAY_HEAD_IP=147.91.12.238 FORT_SKIP_SNP_VERIFY=true FORT_ALLOW_DUMMY=true ./server
```

Environment variables consumed by the server:

| Variable | Default | Description |
|---|---|---|
| `ATLS_ADDR` | `0.0.0.0:9443` | Listen address |
| `RAY_HEAD_IP` | `127.0.0.1` | IP embedded in the issued head TLS cert |
| `FORT_EXPECTED_MEASUREMENT` | *(none — any measurement accepted)* | Required 48-byte SEV-SNP launch measurement as 96 hex chars |
| `FORT_SKIP_SNP_VERIFY` | `false` | Skip AMD VCEK certificate chain check (testing only) |
| `FORT_ALLOW_DUMMY` | `false` | Accept non-SEV (dummy) evidence from plain VMs (testing only) |

Each CVM worker that passes attestation will receive a signed Ray TLS cert.

### 3. Start the Ray Head

```sh
pip install ray scipy numpy
export RAY_USE_TLS=1
export RAY_TLS_SERVER_CERT=fort/server/head.crt
export RAY_TLS_SERVER_KEY=fort/server/head.key
export RAY_TLS_CA_CERT=fort/server/ca.crt
ray start --head --port=6379 --node-ip-address=147.91.12.238 --num-cpus=0
```

The `--node-ip-address` must be the host's LAN IP so that CVM workers can
reach the head through the bridge's default route.

### 4. Launch CVM Workers

```sh
# Pre-auth sudo so all VMs start without password prompts
sudo -v

# Launch N workers (each uses tap(N-1) and IP 192.168.100.(N+1))
./launch-workers.sh 14
```

Each worker's serial console output is written to `worker-logs/worker-N.log`.

Monitor boot progress:

```sh
tail -f worker-logs/worker-*.log
```

Watch workers join the cluster:

```sh
watch -n 3 ray status
```

Workers appear in `ray status` once `ray start` completes inside the VM
(typically 30–60 seconds; Ray is pre-installed in the rootfs).

### 5. Run the Smoke Test

```sh
python fort/test_ray.py
```

Expected output:

```text
Waiting for CVM worker to appear...
Worker connected: 192.168.100.2
double(0) = 0
double(1) = 2
double(21) = 42
double(-5) = -10
All tests passed.
```

### 6. Run the Full Benchmark

```sh
python fort/bench_ray.py --workers 14 --csv results.csv
```

To skip the slow CG Class A case (N=14 000):

```sh
python fort/bench_ray.py --workers 14 --skip-cg-a --csv results.csv
```

#### Benchmark metrics

CSV columns match the MPI `summary.csv` format:

| Column | Description |
|---|---|
| `enroll_ms` | Time from `ray.init()` until all N workers appear |
| `scatter_ms` | `ray.put()` of task inputs on the head |
| `round_ms` | `remote_fn.remote()` → `ray.get()` wall time |
| `compute_ms` | Worker self-reported compute time |
| `gather_ms` | `round_ms − compute_ms` (queue + transfer + return) |
| `scat+gath_ms` | `scatter_ms + gather_ms` (total communication overhead) |
| `speedup` | T(1) / T(N); populated for DGEMM N=1024 only |
| `effic_%` | `speedup / N × 100` |
| `throughput` | GFLOP/s (DGEMM) or Mop/s (EP, CG) |

#### Workloads

| Workload | Sizes | Operations |
|---|---|---|
| DGEMM | N = 512, 1024, 2048 | 2·N³ FLOPs |
| EP | 2²⁴, 2²⁶, 2²⁸ pairs | Gaussian pair generation |
| CG | Class S/W/A | Sparse conjugate gradient |

Speedup S(N) and efficiency E(N) = S(N)/N are reported when `--workers > 1`.

### 7. No-SEV Performance Comparison

Run the same benchmark in plain VMs (no confidential compute) to isolate the
overhead of SEV-SNP memory encryption. Uses the same Buildroot image — the
`atls_snp_attestation=false` kernel parameter switches to dummy attestation.

If SEV workers are still running, stop them first:

```sh
sudo pkill -f qemu-system-x86_64
ray stop
```

#### 7.1 Start the Verifier

```sh
cd fort/server
RAY_HEAD_IP=147.91.12.238 FORT_SKIP_SNP_VERIFY=true FORT_ALLOW_DUMMY=true ./server
```

`FORT_ALLOW_DUMMY=true` is required here because plain VMs send dummy (non-SEV)
attestation evidence. `FORT_SKIP_SNP_VERIFY=true` skips the AMD VCEK chain check.

#### 7.2 Start the Ray Head

```sh
export RAY_USE_TLS=1
export RAY_TLS_SERVER_CERT=fort/server/head.crt
export RAY_TLS_SERVER_KEY=fort/server/head.key
export RAY_TLS_CA_CERT=fort/server/ca.crt
ray start --head --port=6379 --node-ip-address=147.91.12.238 --num-cpus=0
```

#### 7.3 Launch Plain VM Workers

```sh
sudo -v
./launch-workers-nosnp.sh 14
```

Each worker's serial output is written to `worker-logs/worker-nosnp-N.log`.

Monitor boot progress:

```sh
tail -f worker-logs/worker-nosnp-*.log
```

Watch workers join the cluster:

```sh
watch -n 3 ray status
```

#### 7.4 Run the Benchmark

```sh
python fort/bench_ray.py --workers 14 --csv results-nosnp.csv
```

`results-nosnp.csv` has the same columns as `results.csv` (from the SEV run),
so the two files can be compared directly row by row. Any performance
difference isolates the cost of confidential compute.

### 8. Automated Multi-Run Collection

For statistically reliable results run both configurations automatically and
aggregate across runs. Each script restarts Ray between iterations so that
enrollment time is measured fresh on every run.

#### 8.1 Collect SEV-SNP runs

Prerequisites: tap interfaces set up (step 1) and `sudo` pre-authenticated.
The script handles everything else: it builds `compute-measurement` if needed,
computes the expected measurement from the OVMF + kernel + initrd, starts the
verifier with measurement enforcement, and stops it on exit.

```sh
sudo -v
./run-benchmarks-sev.sh 10 14
```

Positional arguments: `RUNS` (default 10), `WORKERS` (default 14),
`REPEAT` inner reps whose median is kept (default 3).

Key environment overrides:

| Variable | Default | Description |
|---|---|---|
| `FORT_OVMF_PATH` | `OVMF.amdsev.fd` in repo root | AMD SEV OVMF firmware binary |
| `FORT_KERNEL_PATH` | `buildroot/output/images/bzImage` | Kernel to include in measurement |
| `FORT_INITRD_PATH` | `buildroot/output/images/rootfs.cpio.gz` | Initramfs to include in measurement |
| `FORT_KERNEL_CMDLINE` | matches `-append` in `launch.sh` | Cmdline string; must be identical across all workers |
| `FORT_VCPU_COUNT` | `2` | Must match `-smp` in `launch.sh` |
| `FORT_VCPU_TYPE` | `EPYC-v4` | Must match `-cpu` in `launch.sh` |

Results are written to `results-sev/run-01.csv` … `run-10.csv`.

#### 8.2 Collect no-SEV runs

Prerequisites: tap interfaces set up (step 1) and `sudo` pre-authenticated.
The script starts the verifier in dummy-accept mode automatically.

```sh
sudo -v
./run-benchmarks-nosnp.sh 10 14
```

Results are written to `results-nosnp/run-01.csv` … `run-10.csv`.

#### 8.3 Aggregate and compare

```sh
python aggregate.py results-sev/ results-nosnp/
```

Writes `results-sev/summary.csv` and `results-nosnp/summary.csv`, then
prints a side-by-side overhead table:

```
Wl     Size    SEV mean  No-SEV mean  Overhead  Unit     Note
DGEMM  512       25.730       13.421     -47.8%  GFLOP/s  ← SEV faster (noise?)
EP     2^28      16.413       19.026     +16.0%  Mop/s    ← SEV overhead
CG     A       1400.829     1499.424      +7.0%  Mop/s    ≈ equal
```

Each `summary.csv` has these additional columns:

| Column | Description |
|---|---|
| `throughput_mean` | Mean throughput across all runs |
| `throughput_std` | Standard deviation |
| `throughput_cv_%` | Coefficient of variation (std / mean × 100) |
| `compute_ms_mean` | Mean worker compute time |
| `compute_ms_std` | Standard deviation of compute time |

**Estimated runtime:** ~3 min/run × 10 runs ≈ 30 min per configuration
(EP 2²⁸ dominates at ~16 s/rep × 3 inner reps).

### 9. Tear Down

```sh
# Stop all CVMs
sudo pkill -f qemu-system-x86_64

# Stop Ray
ray stop
```

The bridge and tap devices persist until the next reboot. To remove them
manually:

```sh
sudo fort/teardown-bridge.sh 14
```

## Key Scripts

| Script                                       | Purpose                                                                           |
|----------------------------------------------|-----------------------------------------------------------------------------------|
| `fort/setup-bridge.sh N`                     | Create bridge `br0` and N tap devices                                             |
| `fort/teardown-bridge.sh N`                  | Remove bridge `br0`, dummy `vnet0`, and N tap devices                             |
| `launch.sh`                                  | Boot one SEV-SNP CVM (`atls_snp_attestation=true`, `kernel-hashes=on`)           |
| `launch-workers.sh N`                        | Boot N SEV-SNP CVMs in parallel                                                   |
| `launch-nosnp.sh`                            | Boot one plain VM (`-cpu EPYC-v4`, `atls_snp_attestation=false`)                 |
| `launch-workers-nosnp.sh N`                  | Boot N plain VMs in parallel (for SEV overhead comparison)                        |
| `fort/server`                                | ATLS verifier + CA: verifies AMD VCEK chain, enforces measurement, issues Ray TLS certs |
| `fort/tools/compute-measurement`             | Compute expected SEV-SNP launch measurement from OVMF + kernel + initrd + cmdline |
| `fort/test_ray.py`                           | Smoke test: `double(x) = x + x`                                                  |
| `fort/bench_ray.py`                          | Full 6-metric HPC benchmark (single run)                                          |
| `run-benchmarks-sev.sh`                      | Collect N independent SEV-SNP runs → `results-sev/run-NN.csv`                    |
| `run-benchmarks-nosnp.sh`                    | Collect N independent no-SEV runs → `results-nosnp/run-NN.csv`                   |
| `aggregate.py`                               | Compute mean ± std across runs; compare SEV vs no-SEV overhead                   |

## Port Assignment

Ray ports are assigned per worker ID to avoid conflicts:

| Worker ID | Node-manager port | Object-manager port | Bridge IP      |
|-----------|-------------------|---------------------|----------------|
| 1         | 6381              | 6382                | 192.168.100.2  |
| 2         | 6383              | 6384                | 192.168.100.3  |
| …         | …                 | …                   | …              |
| 14        | 6407              | 6408                | 192.168.100.15 |
