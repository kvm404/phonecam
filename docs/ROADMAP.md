# PhoneCam Linux Roadmap

## v0.1: Local Wi-Fi MVP

- Android app.
- Linux Go CLI.
- Wi-Fi streaming.
- QR pairing.
- RTP/UDP transport.
- Android hardware H.264 encode.
- GStreamer Linux receiver.
- `v4l2loopback` virtual webcam.
- 720p30 target.
- `phonecam start`, `status`, `stop`, `doctor`, and initial `install`.
- Arch-first install docs.
- Fedora and Ubuntu/Debian docs after Arch path is proven.
- Compatibility validation for Meet, Zoom, Discord, OBS, Chromium, and Firefox.

## v0.2: Reliability And Controls

Design: [`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md).

- Auto-reconnect (in-session resume after a Wi-Fi drop).
- Persistent trusted device pairing (next session without a new QR).
- Better degraded-network behavior (stay live under loss).
- Camera-switch polish (front/back flip already shipped; persist facing and fail safe).
- Resolution leftovers (720p/540p/360p selector already shipped; bitrate-per-preset already shipped).

## v0.3: Desktop Experience

- Tray helper.
- Systemd user background mode.
- Better OBS profiles.
- Phone microphone support.
- Audio sync handling.
- More polished install helper.

## v1.0: Stable Release

- Stable Wi-Fi and USB support.
- Polished Android app.
- Polished CLI/tray experience.
- Distro packages.
- Complete Arch/Fedora/Ubuntu docs.
- Reliable long-session behavior.
- Published compatibility matrix.

## Later (unscheduled)

Deferred from v0.2; not part of the v0.3 desktop slice.

- Torch toggle.
- Latency measurement harness (`docs/BENCHMARKS.md` stays a template until then).
- 15 fps Home toggle (dropped from v0.2; bitrate-per-preset already shipped).

