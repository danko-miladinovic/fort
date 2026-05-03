#!/bin/sh
set -eu

verifier_ip="${FORT_VERIFIER_IP:-10.0.2.2}"
verifier_port="${FORT_VERIFIER_PORT:-9443}"
binaries_dir="${BINARIES_DIR:-${1:?missing Buildroot images directory}}"
script="${binaries_dir}/start-qemu.sh"

cat > "${script}" <<EOF
#!/bin/sh
set -eu

dir=\$(dirname "\$0")
dir=\$(cd "\${dir}" && pwd)

qemu=\${FORT_QEMU:-qemu-system-x86_64}
verifier_ip=\${FORT_VERIFIER_IP:-${verifier_ip}}
verifier_port=\${FORT_VERIFIER_PORT:-${verifier_port}}

exec "\${qemu}" \\
	-M q35 \\
	-cpu max \\
	-m \${FORT_QEMU_MEMORY:-2048M} \\
	-kernel "\${dir}/bzImage" \\
	-initrd "\${dir}/rootfs.cpio.gz" \\
	-append "console=ttyS0 root=/dev/ram0 rw verifier_ip=\${verifier_ip} verifier_port=\${verifier_port}" \\
	-netdev user,id=net0 \\
	-device virtio-net-pci,netdev=net0 \\
	-nographic
EOF

chmod +x "${script}"
