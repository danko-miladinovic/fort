# Fort

To build the kernel and initramfs execute the following commands:
```sh
git clone https://github.com/buildroot/buildroot.git
git clone https://github.com/danko-miladinovic/fort.git

cd buildroot

make BR2_EXTERNAL=../fort fort_defconfig
make menuconfig
make
```
