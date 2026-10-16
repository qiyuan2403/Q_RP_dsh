#!/bin/sh
# Build the JNI library for android/arm64.
#
# jni.h comes from the NDK's sysroot; the compiler is the NDK's clang, reached
# through the emulation setup described in ../docs/build-environment.md.
#
# The script changes into its own directory, because `go build .` resolves the
# package from the working directory and the caller may run it from anywhere.
set -e
cd "$(dirname "$0")"

SDK="${ANDROID_HOME:-/opt/android-sdk}"
NDK="${ANDROID_NDK_HOME:-$SDK/ndk/26.1.10909125}"
TC="$NDK/toolchains/llvm/prebuilt/linux-x86_64"
CC="$TC/bin/aarch64-linux-android29-clang"

export LD_LIBRARY_PATH="/opt/x86run/root/usr/lib:/opt/x86run/root/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

# -z,max-page-size=16384: Google Play requires 16KB page alignment for apps
# targeting API 35+; the linker defaults to 4KB, which fails the check. The
# flag is harmless on 4KB-page devices.
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$CC" \
	go build -buildmode=c-shared \
	-ldflags="-extldflags=-Wl,-z,max-page-size=16384" \
	-o libqrpdsh.so .
