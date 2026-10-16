#!/bin/bash
# Assemble the embedded Node runtime for the Android app.
#
# The runtime comes from the Termux package repository, which has produced
# Bionic-linked Node builds for android/arm64 for years (Node 26.4.0 at the
# time of writing). Downloading release artifacts beats cross-compiling:
# the build is CI-verified upstream, and this machine's qemu-hosted x86-64
# toolchain cannot build Node in reasonable time or memory.
#
# Outputs (both consumed by the Android build, neither committed):
#   jni/libnodebin.so                       node binary, disguised for jniLibs
#   app/src/main/assets/nodebundle_libs.tgz dynamic libs + openssl.cnf
#
# The node binary is a PIE ELF, so Android's packager accepts it as a shared
# object; the app never dlopens it, it execve's the extracted copy.
# The tarball carries lib/ and etc/tls/ prefixes deliberately — NodeRuntime
# extracts it straight into filesDir/node.
set -euo pipefail

REPO="https://packages.termux.dev/apt/termux-main"
ARCH="aarch64"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

HERE="$(cd "$(dirname "$0")" && pwd)"
OUT_BIN="$HERE/jni/libnodebin.so"
OUT_LIBS="$HERE/app/src/main/assets/nodebundle_libs.tgz"

PACKAGES="nodejs libc++ c-ares libicu libsqlite zlib libffi openssl npm"

echo "==> reading the package index"
curl -sL --compressed "$REPO/dists/stable/main/binary-$ARCH/Packages" -o "$WORK/Packages"
grep -c "^Package:" "$WORK/Packages" >/dev/null

mkdir -p "$HERE/jni" "$(dirname "$OUT_LIBS")"

for pkg in $PACKAGES; do
	echo "==> $pkg"
	filename=$(awk -v pkg="$pkg" '
		$1 == "Package:" && $2 == pkg { hit = 1 }
		hit && $1 == "Filename:" { print $2; exit }
		hit && $1 == "Package:" && $2 != pkg { exit }
	' "$WORK/Packages")
	if [ -z "$filename" ]; then
		echo "package not found in index: $pkg" >&2
		exit 1
	fi
	# A colon in the version (epoch) must be URL-encoded in the path.
	url="$REPO/${filename//:/\%3A}"
	curl -sL -o "$WORK/$pkg.deb" "$url"
	mkdir -p "$WORK/x_$pkg"
	(cd "$WORK/x_$pkg" && ar x "$WORK/$pkg.deb" && tar xJf data.tar.xz)
done

ROOTS=$(for pkg in $PACKAGES; do echo "$WORK/x_$pkg/data"; done)

echo "==> node binary"
NODE_BIN=$(find $ROOTS -type f -path "*/bin/node" | head -1)
cp "$NODE_BIN" "$OUT_BIN"

echo "==> dynamic libraries + openssl.cnf"
mkdir -p "$WORK/bundle/lib" "$WORK/bundle/etc/tls"
for root in $ROOTS; do
	find "$root" -name "*.so*" -exec cp -P {} "$WORK/bundle/lib/" \; 2>/dev/null || true
done
CNF=$(find $ROOTS -name "openssl.cnf" | head -1)
cp "$CNF" "$WORK/bundle/etc/tls/openssl.cnf"

tar czf "$OUT_LIBS" -C "$WORK/bundle" lib etc

echo
echo "assembled:"
ls -la "$OUT_BIN" "$OUT_LIBS" | awk '{printf "  %s  %s (%.1f MB)\n", $1, $9, $5/1048576}'
