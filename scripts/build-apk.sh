#!/usr/bin/env bash
# Build the Android app to an installable APK.
#
# Containerised for the same reason as the .aar: Gradle needs a JDK the core
# does not, and the host SDK is mounted rather than re-downloaded. The SDK is
# mounted read-write because Gradle writes licence acceptances and may fetch
# platform components.
#
#   make apk
set -euo pipefail
cd "$(dirname "$0")/.."

SDK=${ANDROID_SDK:-$HOME/Android/Sdk}
[ -d "$SDK" ] || { echo "no Android SDK at $SDK — set ANDROID_SDK"; exit 1; }
[ -f android/logosvpn.aar ] || { echo "run 'make aar' first"; exit 1; }
# A .aar older than the Go sources is the stale-artefact trap: the APK builds
# against yesterday's binding and fails on a function that exists.
if [ -n "$(find mobile internal -name '*.go' -newer android/logosvpn.aar -print -quit 2>/dev/null)" ]; then
    echo "WARNING: android/logosvpn.aar is older than the Go sources — run 'make aar'" >&2
fi

# RELEASE=1 builds an unsigned release APK for signing elsewhere.
if [ "${RELEASE:-0}" = "1" ]; then
    TASK=assembleRelease
    ARGS="-PversionCode=${VERSION_CODE:-1} -PversionName=${VERSION_NAME:-0.1-prototype}"
    BUILT=android/app/build/outputs/apk/release/app-release-unsigned.apk
    OUTPUT=android/shrooms-unsigned.apk
else
    TASK=assembleDebug
    ARGS=""
    BUILT=android/app/build/outputs/apk/debug/app-debug.apk
    OUTPUT=android/shrooms.apk
fi

echo "==> building the APK ($TASK)"
docker run --rm \
    -v "$PWD:/src" -v "$SDK:/sdk" \
    -v "$PWD/.gradle-cache:/gradle" \
    -w /src/android \
    -e ANDROID_HOME=/sdk -e ANDROID_SDK_ROOT=/sdk -e GRADLE_USER_HOME=/gradle \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    -e GRADLE_TASK="$TASK" -e GRADLE_ARGS="$ARGS" \
    eclipse-temurin:17-jdk bash -euc '
        # Gradle is fetched by the wrapper if present, else installed here.
        if [ ! -x ./gradlew ]; then
            apt-get update -qq && apt-get install -y -qq wget unzip >/dev/null 2>&1
            wget -q https://services.gradle.org/distributions/gradle-8.11.1-bin.zip -O /tmp/g.zip
            unzip -q /tmp/g.zip -d /opt && ln -sf /opt/gradle-8.11.1/bin/gradle /usr/local/bin/gradle
            GRADLE=gradle
        else
            GRADLE=./gradlew
        fi
        $GRADLE --no-daemon "$GRADLE_TASK" $GRADLE_ARGS
        chown -R "$HOST_UID:$HOST_GID" /src/android /gradle 2>/dev/null || true
    '

[ -f "$BUILT" ] || { echo "no APK produced at $BUILT"; exit 1; }
cp "$BUILT" "$OUTPUT"
echo
echo "==> $OUTPUT ($(du -h "$OUTPUT" | cut -f1))"
if [ "${RELEASE:-0}" = "1" ]; then
    echo "    unsigned; sign and publish with: make fdroid"
else
    echo "    install with: adb install -r $OUTPUT"
fi
