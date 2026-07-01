# PhoneCam Linux CLI

This module contains the Linux `phonecam` command.

## Current Scope

The first implementation slice provides:

- command routing for `start`, `status`, `stop`, `doctor`, `install`, `version`,
  and `help`,
- a testable `phonecam doctor` foundation,
- install hints for Arch, Fedora, and Ubuntu/Debian.

The media receiver, pairing server, QR rendering, and process supervision are
not implemented yet.

## Development

```sh
cd linux-cli
go test ./...
go run ./cmd/phonecam doctor
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

