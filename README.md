# PhoneCam

Use your Android phone as a webcam on Linux, over your local network. It shows
up in Meet, Zoom, Discord, OBS, and browsers as a normal `v4l2loopback` camera.

```text
Android camera → H.264 → RTP/UDP over LAN → GStreamer → v4l2loopback → your apps
```

## Install

**1. Linux CLI** (needs [Go](https://go.dev/dl/) 1.22+):

```sh
go install github.com/kvm404/phonecam/linux-cli/cmd/phonecam@latest
```

This puts `phonecam` in `$(go env GOPATH)/bin` — add that to your `PATH` if it
isn't already. (Prefer not to use Go? Grab a prebuilt binary from
[Releases](https://github.com/kvm404/phonecam/releases).)

Then install the Linux dependencies (GStreamer, `v4l2loopback`) and check your
setup:

```sh
phonecam install   # prints the exact packages, modprobe, and firewall commands
phonecam doctor     # verifies everything is ready
```

**2. Android app:** download the APK from
[Releases](https://github.com/kvm404/phonecam/releases) and install it (you may
need to allow installing from unknown sources).

## Usage

```sh
phonecam start      # on the laptop — shows a QR code
```

1. Open the PhoneCam app and **Scan QR**.
2. You're live. Pick **PhoneCam** as the camera in Meet, Zoom, Discord, or OBS.

`phonecam status` shows whether it's running; `phonecam stop` stops it.

## Requirements

- Linux with GStreamer and `v4l2loopback` (`phonecam install` sets these up).
- Android 10+ (minSdk 26).
- Phone and laptop on the same Wi-Fi / LAN.

## Status

**v0.3.0.** Working Wi-Fi webcam with QR pairing, trusted reconnect, and
in-session recovery. v0.3.0 adds camera zoom controls on the LIVE screen
(+ / − / 1x in 0.25x steps, clamped to the lens' real range) and an
always-visible viewfinder — the hide-preview toggle is gone. Verified on
Arch Linux + vivo, and Fedora KDE aarch64 +
OBS coexistence + Motorola Edge 60 Fusion. Other distros and phones are still
welcome. Video only (no audio), and the LAN stream is **unencrypted**,
so use it on trusted networks only. There is no account, cloud, or relay —
video goes straight from phone to laptop.

## Docs & License

Design and product docs are in [`docs/`](docs/). Licensed under
[MIT](LICENSE). Omarchy bar plugin: [`omarchy-plugin/README.md`](omarchy-plugin/README.md).
