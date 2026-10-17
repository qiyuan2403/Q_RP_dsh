# Android build environment

How this host builds an Android APK, and why it needs an unusual setup.

## The problem

The host is **aarch64**. The Android SDK's build tools (`aapt2`, `zipalign`) and
the NDK's toolchain (`clang`) are **x86-64** binaries. They cannot execute
directly — the kernel reports `Exec format error`.

```
$ /opt/android-sdk/build-tools/34.0.0/aapt2 version
aapt2: 无法执行二进制文件
```

## The solution: emulation

`qemu-user` emulates x86-64, and `binfmt_misc` makes the kernel dispatch x86-64
binaries to it transparently, so the tools run as ordinary commands.

```bash
pacman -S qemu-user

# Register the interpreter with the kernel. binfmt_misc must be mounted first;
# inside a container it often is not.
mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc
echo ':qemu-x86_64:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/usr/bin/qemu-x86_64:POCF' \
  > /proc/sys/fs/binfmt_misc/register
```

## The runtime libraries

`binfmt_misc` runs `qemu-x86_64 <binary>`, and qemu then invokes the binary's
**own** ELF interpreter, `/lib64/ld-linux-x86-64.so.2`. That loader searches
paths compiled into **it**, not this host's paths, so an x86-64 glibc must be
present.

The packages are extracted from the Arch x86-64 repositories rather than
installed: this is an aarch64 system, and `pacman` cannot install foreign
architecture packages into it.

```bash
M=https://geo.mirror.pkgbuild.com
mkdir -p /opt/x86run/root

for pkg in glibc libgcc zlib zstd xz bzip2 libxml2; do
  # resolve the filename from the repository database, then extract
  curl -sL "$M/core/os/x86_64/$filename" | tar --use-compress-program=unzstd -x -C /opt/x86run/root
done
```

`libgcc` matters specifically: `gcc-libs` in Arch is a **metadata-only** split
package, so fetching it yields no `libgcc_s.so.1`.

The loader must then be told where they are. Its own configuration is the right
place, because `binfmt_misc` does not pass `--library-path`:

```bash
cat > /etc/ld.so.conf.d/x86_64-emulation.conf <<'CONF'
/opt/x86run/root/usr/lib
/opt/x86run/root/lib
CONF
```

In practice `LD_LIBRARY_PATH` is also exported by the build script, which is
what makes the tools work regardless of the loader's configuration.

## Components

| Component | Version | How |
|---|---|---|
| JDK | 17 | distribution package |
| cmdline-tools | 12.0 | `commandlinetools-linux-11076708_latest.zip` |
| build-tools | 34.0.0 | `sdkmanager` |
| platforms | android-34 | `sdkmanager` |
| NDK | 26.1.10909125 | `sdkmanager` |
| qemu-user | 11.1.2 | distribution package |

`dl.google.com` was unreachable from this host (TLS reset mid-download); the
SDK came from `mirrors.cloud.tencent.com/AndroidSDK/` instead.

## Building

```bash
cd android
./build.sh
```

The script compiles the Go harness as an `android/arm64` shared library, stages
the JNI and Node runtime artifacts in the app's standard source directories,
then runs Gradle's `:app:assembleDebug` task for the Compose application.

## Why the harness is a shared library

Android 10 (API 29) forbids executing files in an app's home directory. A Go
binary shipped in `assets/` and unpacked at runtime therefore cannot be
`execve`'d. A shared library in `lib/arm64-v8a/` is loaded by the dynamic
linker instead, which is permitted, so the harness is built with
`-buildmode=c-shared` and reached through JNI.

That choice costs one thing: `c-shared` requires CGO, which is why the NDK
toolchain is needed at all. A pure-Go build needs none of this emulation.
