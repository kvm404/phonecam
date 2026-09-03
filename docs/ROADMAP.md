# PhoneCam Linux Roadmap

**v0.3.0 is the current release.** Shipped slices below are history. There
are no roadmap buckets — no v0.4, v1.0, or Later. USB, audio, tray, systemd,
and torch stay cancelled. Distro packages are no longer cancelled: GitHub
release `.deb` / `.rpm` / Arch `.pkg.tar.zst` is the first packaging cut.

## Current

Omarchy bar plugin (`omarchy-plugin/`, id `io.github.kvm404.phonecam`): start/stop, pairing QR, status, doctor, trust. Wraps the v0.2 CLI. Not USB, audio, tray, or systemd.

Distro packages: tag workflow attaches `.deb`, `.rpm`, and `.pkg.tar.zst` for amd64 and arm64 (Depends on distro GStreamer plugins + v4l2loopback). Package installs `/usr/bin/phonecam` only; `sudo phonecam setup` loads the loopback. No PPA, COPR, Snap, Flatpak, or v4l2loopback fork. In-repo `packaging/aur/PKGBUILD` is AUR-ready `phonecam-bin` and is not pushed to aur.archlinux.org.

## v0.3: Zoom And Always-On Preview

- Camera zoom controls on the LIVE screen: 0.25x steps, reset to 1x on stream start and flip, hidden when the lens has no zoom range ([ADR 0002](adr/0002-camera-zoom-controls.md)).
- The viewfinder is always visible while streaming; the hide-preview toggle and its persisted preference are removed.

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
