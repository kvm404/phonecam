# PhoneCam Linux Roadmap

**v0.2.0 is the product.** Shipped slices below are history. There is no v0.3,
v1.0, or Later bucket. USB, audio, tray, systemd, torch, distro packages, and
the other unscheduled items that used to live here are cancelled — not deferred.

## Current

Omarchy bar plugin (`omarchy-plugin/`, id `io.github.kvm404.phonecam`): start/stop, pairing QR, status, doctor, trust. Wraps the v0.2 CLI. Not USB, audio, tray, or systemd.

Camera zoom controls on the LIVE screen (`feat/camera-zoom`, PR pending): 0.25x steps, reset to 1x on stream start and flip, hidden when the lens has no zoom range.

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

## v0.2: Reliability And Controls

Design: [`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md).

- Auto-reconnect (in-session resume after a Wi-Fi drop).
- Persistent trusted device pairing (next session without a new QR).
- Better degraded-network behavior (stay live under loss).
- Camera-switch polish (front/back flip already shipped; persist facing and fail safe).
- Resolution leftovers (720p/540p/360p selector already shipped; bitrate-per-preset already shipped).
