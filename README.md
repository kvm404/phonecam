# PhoneCam

Use your Android phone as a webcam on Linux, over your local network. It shows
up in Meet, Zoom, Discord, OBS, and browsers as a normal `v4l2loopback` camera.

```text
Android camera → H.264 → RTP/UDP over LAN → GStreamer → v4l2loopback → your apps
```

## Install

**1. Linux CLI** — download a `.deb`, `.rpm`, or `.pkg.tar.zst` from
[Releases](https://github.com/kvm404/phonecam/releases) and install it:

```sh
# Debian / Ubuntu (amd64 example; use *_arm64.deb on aarch64)
sudo apt install ./phonecam_*_amd64.deb

# Fedora: enable RPM Fusion free first. v4l2loopback and
# gstreamer1-plugin-libav are not in stock Fedora, so the RPM Requires
# will fail until RPM Fusion is enabled.
sudo dnf install https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-$(rpm -E %fedora).noarch.rpm
sudo dnf install ./phonecam-*.x86_64.rpm   # or .aarch64.rpm

# Arch
sudo pacman -U phonecam-*-x86_64.pkg.tar.zst   # or *-aarch64.pkg.tar.zst
```

Then load the virtual camera. The package only installs `/usr/bin/phonecam`;
it does not `modprobe` or write loopback options (that would break an existing
OBS layout):

```sh
sudo phonecam setup   # installs remaining deps if needed, loads /dev/video10
phonecam doctor       # verifies everything is ready
```

`phonecam install` is print-only: it prints package, modprobe, and firewall
hints without changing the system.

Fallback if you prefer Go (needs [Go](https://go.dev/dl/) 1.22+):

```sh
go install github.com/kvm404/phonecam/linux-cli/cmd/phonecam@latest
sudo phonecam setup
```

This puts `phonecam` in `$(go env GOPATH)/bin` — add that to your `PATH` if it
isn't already. Prebuilt binaries (`phonecam-linux-amd64` / `phonecam-linux-arm64`)
are on the same [Releases](https://github.com/kvm404/phonecam/releases) page.

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

- Linux with GStreamer and `v4l2loopback` (`sudo phonecam setup` installs these).
- Android 10+ (minSdk 26).
- Phone and laptop on the same Wi-Fi / LAN.

## Status

**v0.3.1.** Working Wi-Fi webcam with QR pairing, trusted reconnect, and
in-session recovery. v0.3.0 adds camera zoom controls on the LIVE screen
(+ / − / 1x in 0.25x steps, clamped to the lens' real range) and an
always-visible viewfinder — the hide-preview toggle is gone. v0.3.1 adds
`sudo phonecam setup` and GitHub `.deb` / `.rpm` / `.pkg.tar.zst`. The
Android app is still the v0.3.0 APK. Verified on
Arch Linux + vivo, and Fedora KDE aarch64 +
OBS coexistence + Motorola Edge 60 Fusion. Other distros and phones are still
welcome. Video only (no audio), and the LAN stream is **unencrypted**,
so use it on trusted networks only. There is no account, cloud, or relay —
video goes straight from phone to laptop.

## Docs & License

Design and product docs are in [`docs/`](docs/). Licensed under
[MIT](LICENSE). Omarchy bar plugin: [`omarchy-plugin/README.md`](omarchy-plugin/README.md).
