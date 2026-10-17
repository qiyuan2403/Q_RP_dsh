#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
GRADLE="${GRADLE:-$HOME/.gradle/wrapper/dists/gradle-9.7.1-bin/1w1c7tv4s851m17nbqdsro2tv/gradle-9.7.1/bin/gradle}"

# The host's Android SDK tools are x86-64 under qemu-user.
export LD_LIBRARY_PATH="/opt/x86run/root/usr/lib:/opt/x86run/root/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

cd "$HERE"
echo "==> building the Go JNI library"
./jni/build.sh

if [[ ! -f jni/libnodebin.so || ! -f app/src/main/assets/nodebundle_libs.tgz ]]; then
	if [[ "${BUILD_NODE:-0}" != "1" ]]; then
		echo "Node runtime artifacts are missing. Run android/nodebundle.sh, then retry." >&2
		exit 1
	fi
	./nodebundle.sh
fi

mkdir -p app/src/main/jniLibs/arm64-v8a
if [[ -f jni/libqrpdsh.so ]]; then
	cp jni/libqrpdsh.so app/src/main/jniLibs/arm64-v8a/libqrpdsh.so
fi
if [[ -f jni/libnodebin.so ]]; then
	cp jni/libnodebin.so app/src/main/jniLibs/arm64-v8a/libnodebin.so
fi
if [[ ! -f app/src/main/jniLibs/arm64-v8a/libqrpdsh.so || ! -f app/src/main/jniLibs/arm64-v8a/libnodebin.so || ! -f app/src/main/assets/nodebundle_libs.tgz ]]; then
	echo "Required JNI or Node runtime artifact is missing; build stopped before Gradle packaging." >&2
	exit 1
fi

if [[ ! -x "$GRADLE" ]]; then
	echo "Gradle executable not found: $GRADLE" >&2
	exit 1
fi
echo "==> assembling the Compose Android app"
"$GRADLE" --offline --no-daemon :app:assembleDebug

APK="$HERE/app/build/outputs/apk/debug/app-debug.apk"
if [[ ! -f "$APK" ]]; then
	echo "Gradle completed without producing $APK" >&2
	exit 1
fi
echo "APK: $APK ($(stat -c%s "$APK") bytes)"
