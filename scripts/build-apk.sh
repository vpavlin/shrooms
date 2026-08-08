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

echo "==> building the APK"
docker run --rm \
    -v "$PWD:/src" -v "$SDK:/sdk" \
    -v "$PWD/.gradle-cache:/gradle" \
    -w /src/android \
    -e ANDROID_HOME=/sdk -e ANDROID_SDK_ROOT=/sdk -e GRADLE_USER_HOME=/gradle \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
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
        $GRADLE --no-daemon assembleDebug
        chown -R "$HOST_UID:$HOST_GID" /src/android /gradle 2>/dev/null || true
    '

APK=android/app/build/outputs/apk/debug/app-debug.apk
[ -f "$APK" ] || { echo "no APK produced"; exit 1; }
cp "$APK" android/logos-vpn.apk
echo
echo "==> android/logos-vpn.apk ($(du -h android/logos-vpn.apk | cut -f1))"
echo "    install with: adb install -r android/logos-vpn.apk"
