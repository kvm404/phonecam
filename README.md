# PhoneCam Linux

Use your Android phone as a high-quality virtual webcam on Linux, over your
local network, appearing to Meet/Zoom/Discord/OBS as a normal `v4l2loopback`
camera.

```text
Android CameraX
  -> MediaCodec H.264 hardware encode
  -> RTP/UDP over LAN
  -> GStreamer receiver on Linux
  -> v4l2loopback virtual camera
  -> Meet / Zoom / Discord / OBS / browsers
```

## Status & Scope

PhoneCam is **v0.1** and early. It has been verified on **one** reference setup
only:

- **Linux:** Arch Linux.
- **Android:** one vivo device (Android 10+).

Other distros and phones are **untested** — they may work, and community help to
confirm them is very welcome. See the
[Compatibility Matrix](docs/COMPATIBILITY_MATRIX.md).

**Not yet:** audio (video-only for now) and on-the-wire encryption.

## Security & Privacy

- **Local network only.** There is no account, no cloud, and no video relay —
  the stream goes directly from your phone to your Linux machine over the LAN.
- **The RTP video stream is currently UNENCRYPTED** on the local network.
- **Pairing uses a short-lived, single-use QR token** that the phone scans off
  your screen.
- Use PhoneCam on **trusted networks only** (home/office Wi-Fi), not on shared
  or public networks.

## Install (Linux CLI)

**Option A — prebuilt binary (recommended):** download the binary for your
architecture from the [Releases](https://github.com/kvm404/phonecam/releases)
page, then:

```sh
chmod +x phonecam-linux-amd64
sudo mv phonecam-linux-amd64 /usr/local/bin/phonecam
```

**Option B — build from source (Go 1.22+):**

```sh
cd linux-cli
go build -o phonecam ./cmd/phonecam
```

Then check your setup and install the Linux dependencies (GStreamer, firewall
rules, `v4l2loopback`):

```sh
phonecam doctor
phonecam install
```

## Install (Android)

Sideload the APK from the
[Releases](https://github.com/kvm404/phonecam/releases) page (you may need to
enable installing from unknown sources), or build it yourself:

```sh
cd android
./gradlew assembleDebug
```

The debug APK is written to `app/build/outputs/apk/debug/app-debug.apk`.

## Quickstart

1. On the laptop, run `phonecam start`. A QR code is shown for pairing.
2. Open the PhoneCam app on your phone and **Scan QR**.
3. You're live — the phone camera is now streaming to the virtual webcam.
4. In Meet, Zoom, Discord, or OBS, pick **PhoneCam** as the camera.

Use `phonecam status` to see whether it's running and `phonecam stop` to stop
it.

## Requirements

- **Linux** with GStreamer and `v4l2loopback`. Run `phonecam install` for the
  exact per-distro packages, the `modprobe` command, and firewall rules.
- **Android 10+** (minSdk 26).
- Phone and laptop on the **same LAN**.

## Documentation

Design and product docs live in [`docs/`](docs/):

- [Product Requirements](docs/PRD.md)
- [Technical Design](docs/TECHNICAL_DESIGN.md)
- [Test Strategy](docs/TEST_STRATEGY.md)
- [Benchmarks](docs/BENCHMARKS.md)
- [Compatibility Matrix](docs/COMPATIBILITY_MATRIX.md)
- [Roadmap](docs/ROADMAP.md)
- [Contributing](CONTRIBUTING.md)

## License

MIT — see [LICENSE](LICENSE).
