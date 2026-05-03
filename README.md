# Fort

Fort is a small ATLS/Exported Authenticator demo for a guest client and a
host verifier.

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
- `fort-client.service`, which starts the client on boot with systemd.
- `board/fort/post-image.sh`, which generates `output/images/start-qemu.sh`.

Build the kernel and initramfs with:

```sh
git clone https://github.com/buildroot/buildroot.git
git clone https://github.com/danko-miladinovic/fort.git

cd buildroot

make BR2_EXTERNAL=../fort/buildroot fort_defconfig
make
```

`make menuconfig` is optional if you want to inspect or change the generated
configuration.

## Run The Verifier

Start the server on the host before booting the guest:

```sh
cd fort/server
ATLS_ADDR=0.0.0.0:9443 go run .
```

The server verifies the client attestation during the ATLS bootstrap. After the
client connects, the server prints:

```text
Hello World!
```

## Boot The Client Guest

After Buildroot finishes, run the generated QEMU launcher:

```sh
cd buildroot
output/images/start-qemu.sh
```

The generated QEMU command adds the verifier address to the kernel command line:

```text
verifier_ip=10.0.2.2 verifier_port=9443
```

With QEMU user networking, `10.0.2.2` points back to the host, so the default
guest connects to the server shown above. To point the guest at another verifier:

```sh
FORT_VERIFIER_IP=192.0.2.10 FORT_VERIFIER_PORT=9443 output/images/start-qemu.sh
```

Inside the guest, `fort/client` resolves the verifier address in this order:

1. `ATLS_ADDR`
2. `VERIFIER_IP` and `VERIFIER_PORT`
3. kernel command line `verifier_ip` and `verifier_port`
4. `127.0.0.1:9443`

The Buildroot service currently sets `ATLS_USE_SEV_SNP_ATTESTATION=false`, so
the client uses dummy attestation evidence by default. Set it to `true` in
`buildroot/package/fort-client/fort-client.service` when running in an
environment with SEV-SNP guest attestation support.
