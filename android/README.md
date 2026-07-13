# PhoneCam Android

Native Kotlin Android app that turns an Android phone into a LAN webcam for
Linux. This scaffold provides a CameraX preview; H.264 encode + RTP streaming
land in later milestones.

## Prerequisites

- JDK 17 (the build breaks on newer JDKs due to AGP).
- Android SDK with platform `android-35` and build-tools `35.0.0`.
- Set the SDK location in `local.properties`:

  ```properties
  sdk.dir=/home/krvm/Android/Sdk
  ```

## Build

```bash
JAVA_HOME=/usr/lib/jvm/java-17-openjdk ./gradlew assembleDebug
```

The debug APK is written to `app/build/outputs/apk/debug/app-debug.apk`.

## Install

```bash
adb install -r app/build/outputs/apk/debug/app-debug.apk
```
