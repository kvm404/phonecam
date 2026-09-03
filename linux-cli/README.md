# PhoneCam Linux CLI

This module contains the Linux `phonecam` command.

## Current Scope

The first implementation slice provides:

- command routing for `start`, `status`, `stop`, `doctor`, `setup`, `install`,
  `version`, and `help`,
- a testable `phonecam doctor` foundation,
- `phonecam setup` to install GStreamer + v4l2loopback and persist `/dev/video10`
  (requires root; `--dry-run` prints the plan),
- a `phonecam start` foundation that creates a pairing session and serves the
  control API,
- print-only install hints for Arch, Fedora, and Ubuntu/Debian.

The media receiver, terminal QR rendering, v4l2loopback lifecycle, and
GStreamer process supervision are not implemented yet.

## Development

```sh
cd linux-cli
go test ./...
go run ./cmd/phonecam doctor
go run ./cmd/phonecam setup --dry-run
```

## Doctor Checks

`phonecam doctor` currently checks:

- `gst-launch-1.0`,
- `gst-inspect-1.0`,
- `modprobe`,
- loaded `v4l2loopback` module,
- `/dev/video10`,
- write access to `/dev/video10`,
- desktop session hints,
- distro family hints,
- LAN privacy warning.
