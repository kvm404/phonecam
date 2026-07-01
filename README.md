# PhoneCam Linux

PhoneCam Linux is an open-source, Linux-first tool for using an Android phone
camera as a high-quality virtual webcam on Linux.

The v0.1 target is a local-network Android camera pipeline that appears to Linux
meeting and recording apps as a normal `v4l2loopback` webcam.

```text
Android CameraX
  -> MediaCodec H.264 hardware encode
  -> RTP/UDP over LAN
  -> GStreamer receiver on Linux
  -> v4l2loopback virtual camera
  -> Meet / Zoom / Discord / OBS / browsers
```

## v0.1 Goals

- 720p30 video.
- Under 180 ms glass-to-browser latency on good Wi-Fi.
- Under 25% laptop CPU on a midrange Linux machine.
- Stable 90-minute sessions.
- Visible in Google Meet, Zoom, Discord, OBS, Chromium, and Firefox.
- No account, cloud video relay, or default TURN dependency.
- Excellent `phonecam doctor` diagnostics for Linux setup issues.

## Planned Components

- `android/`: Kotlin Android app using CameraX and MediaCodec.
- `linux-cli/`: Go CLI for pairing, diagnostics, process lifecycle, and
  GStreamer orchestration.
- `docs/`: product, technical, testing, roadmap, and workflow documentation.

## Planned CLI

```text
phonecam start
phonecam status
phonecam stop
phonecam doctor
phonecam install
```

Future service-mode commands are planned after the local Wi-Fi MVP:

```text
phonecam service enable
phonecam service disable
```

## Project Status

This repository is in planning/pre-implementation. See:

- [Product Requirements](docs/PRD.md)
- [Technical Design](docs/TECHNICAL_DESIGN.md)
- [Test Strategy](docs/TEST_STRATEGY.md)
- [Benchmarks](docs/BENCHMARKS.md)
- [Compatibility Matrix](docs/COMPATIBILITY_MATRIX.md)
- [Roadmap](docs/ROADMAP.md)
- [GitHub Workflow](CONTRIBUTING.md)
