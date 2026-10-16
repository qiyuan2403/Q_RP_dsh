#!/bin/bash
# Build the Android APK.
#
# The build runs on an aarch64 host, but the Android SDK and NDK ship x86-64
# binaries. They execute through qemu-user, registered with binfmt_misc, using
# an extracted x86-64 runtime — see docs/android-build.md for how that is set
# up. Everything else is the standard aapt2/d8/zipalign/apksigner chain.
set -euo pipefail

SDK="${ANDROID_HOME:-/opt/android-sdk}"
NDK="${ANDROID_NDK_HOME:-$SDK/ndk/26.1.10909125}"
BT="$SDK/build-tools/34.0.0"
PLATFORM="$SDK/platforms/android-34/android.jar"
API=29
TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/linux-x86_64"
CC="$TOOLCHAIN/bin/aarch64-linux-android${API}-clang"

# The emulated x86-64 tools need their own runtime on the loader path.
export LD_LIBRARY_PATH="/opt/x86run/root/usr/lib:/opt/x86run/root/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

cd "$(dirname "$0")"
OUT=build
rm -rf "$OUT"
mkdir -p "$OUT"/{compiled,dex,apk}

echo "==> 1/7 building the Go shared library (arm64)"
./jni/build.sh

echo "==> 2/7 compiling Java"
javac -source 8 -target 8 -bootclasspath "$PLATFORM" \
	-d "$OUT/compiled" $(find app/src/main/java -name '*.java')

echo "==> 3/7 dexing"
"$BT/d8" --lib "$PLATFORM" --min-api "$API" --output "$OUT/dex" \
	$(find "$OUT/compiled" -name '*.class')

echo "==> 4/7 compiling resources"
"$BT/aapt2" compile --dir app/src/main/res -o "$OUT/res.zip"

echo "==> 5/7 linking resources"
"$BT/aapt2" link -o "$OUT/apk/base.apk" -I "$PLATFORM" \
	--manifest app/AndroidManifest.xml \
	-A app/src/main/assets \
	--min-sdk-version "$API" --target-sdk-version 34 "$OUT/res.zip"

echo "==> 6/7 assembling"
cd "$OUT/apk"
cp base.apk app-unsigned.apk
zip -q -j app-unsigned.apk ../dex/classes.dex
mkdir -p lib/arm64-v8a
cp ../../jni/libqrpdsh.so lib/arm64-v8a/
# The embedded Node runtime rides in jniLibs as a disguised .so: Android only
# extracts that directory, and the extracted path is executable by the app
# (verified on corot and PHJ110). The 16KB-alignment rule does not apply to
# this entry because the app dlopens nothing from it — it is execve'd.
cp ../../jni/libnodebin.so lib/arm64-v8a/
zip -q -r app-unsigned.apk lib
cd ../..


echo "==> 7/7 aligning and signing"
"$BT/zipalign" -f -p 4 "$OUT/apk/app-unsigned.apk" "$OUT/apk/app-aligned.apk"

if [ ! -f keys/debug.jks ]; then
	echo "    generating a debug keystore"
	mkdir -p keys
	keytool -genkeypair -keystore keys/debug.jks -storepass android -keypass android \
		-alias qrpdsh -keyalg RSA -keysize 2048 -validity 10000 \
		-dname "CN=Q_RP_dsh, OU=dev, O=sta1n, C=CN" >/dev/null 2>&1
fi

"$BT/apksigner" sign --ks keys/debug.jks --ks-pass pass:android --key-pass pass:android \
	--out "$OUT/apk/qrpdsh.apk" "$OUT/apk/app-aligned.apk"
"$BT/apksigner" verify --verbose "$OUT/apk/qrpdsh.apk" | head -1

echo
echo "APK: $(pwd)/$OUT/apk/qrpdsh.apk ($(stat -c%s "$OUT/apk/qrpdsh.apk") bytes)"
