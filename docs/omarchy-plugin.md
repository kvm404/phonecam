# Omarchy plugin — status for maintainers

This is a **design-only** contribution. There is no plugin code in this PR.

PhoneCam already appears in Chromium, Discord, Meet, Zoom, and OBS as the
`v4l2loopback` device `PhoneCam` (`/dev/video10`). No Chrome extension is
needed. What is missing is a desktop UI: today `phonecam start` is a
foreground CLI that prints a terminal QR.

The proposed UI is an **Omarchy (Quickshell) bar plugin** that wraps the
existing CLI and loopback HTTP API. It does not reimplement RTP or GStreamer.

Full spec: [`omarchy-plugin-design.md`](omarchy-plugin-design.md).

## Done

- Mapped the Linux CLI, control HTTP API, pairing QR payload, session file,
  stop semantics, and doctor checks against `linux-cli/`.
- Wrote an Omarchy plugin design (`raja.phonecam`): service + bar widget,
  start/stop, QR from `GET /pairing`, `/status` polling, doctor, trust.
- Iterated the design against a separate reviewer. Last fix: pending-phone
  detect under `--require-approval` must use a **rolling raw stdout buffer**
  (`scanPendingApproval`); the CLI prompt has no trailing newline, so
  line-oriented `SplitParser` cannot work.

## Not done

- Plugin source (`manifest.json`, QML, `bin/phonecam-qr`).
- Independent re-review of that last stdout-buffer fix.
- `omarchy plugin validate` / bar install on a live Omarchy host.

## Suggested remaining work

Follow the PR plan at the bottom of the design doc:

1. Scaffold the plugin repo (or `omarchy-plugin/` in this tree) + manifest.
2. Model.js + QR helper + tests.
3. Service: discover `phonecam`, supervise `start`/`stop`, poll `/status`.
4. Bar + panel UX: QR, doctor, trust, LAN privacy line.

Default product path stays auto-approve. Do not auto-start at login.
