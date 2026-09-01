# Omarchy bar plugin

Thin operate UI for PhoneCam on Omarchy/Hyprland. It wraps the existing CLI
and loopback HTTP API. It does not change `linux-cli`, Android, or the
PhoneCam protocol.

Plugin id: `io.github.kvm404.phonecam` (not `raja.phonecam`; `omarchy.*` is
reserved). Version 0.1.0. Kinds: `service` + `bar-widget`.

## Layout

```
omarchy-plugin/
  manifest.json
  Model.js                 # parse/phase helpers; node-testable, no Qt
  service/Service.qml      # start/stop, HTTP client, QR helper
  widget/PhoneCamBar.qml   # bar icon + panel
  bin/phonecam-qr          # stdin JSON → 0/1 QR matrix
  bin/phonecam-preview     # live JPEG ping-pong from /dev/video10
  test/model-test.js
  install.sh
```

The widget finds the service with
`bar?.shell?.serviceFor("io.github.kvm404.phonecam")`.

## Wrap only

Allowed argv (no `sh -c` with interpolated secrets):

- `$bin start --control-port N --rtp-port M` (CLI auto-approves; never
  `--require-approval`)
- `$bin stop`
- `$bin doctor`
- `$bin version` (optional)

HTTP: the plugin is always a **loopback client**
(`http://127.0.0.1:<controlPort>/…`), never a bind. Use `curl -sS --max-time 2`
with exact argv. One in-flight probe. Do not log bodies that may contain
tokens.

| Endpoint | Bind vs client | Use |
|---|---|---|
| GET `/healthz` | LAN `{ok:true}` | liveness |
| GET `/pairing` | **loopback** | QR payload |
| GET `/status` | LAN | phase; strip `resume_token` / `pairing_secret` |
| GET `/trust` | loopback | trusted phones |
| DELETE `/trust/{id}` | loopback | revoke |
| POST `/approve` | loopback | unused (auto-approve) |

## Install (copy, not `plugin add` of phonecam.git)

`omarchy plugin add <git url>` clones a URL and validates the **clone root**.
This repo’s root has no `manifest.json` and must not grow one.

```sh
./omarchy-plugin/install.sh
```

The script runs `omarchy plugin validate` first, copies into
`~/.config/omarchy/plugins/io.github.kvm404.phonecam` (files, not a plugin-dir
symlink), rescans, and prints `omarchy plugin enable io.github.kvm404.phonecam`.
It does not enable on its own.

## Operate rules

- **No auto-start** at shell login. The user presses Start.
- If `$XDG_RUNTIME_DIR/phonecam/session.json` exists and `/healthz` answers,
  **adopt** that session. Do not start a second receiver.
- Supervise `start` with
  `setpriv --pdeathsig TERM <bin> start --control-port … --rtp-port …`.
- **Redact stdout** of `phonecam start` (QR + pairing JSON including `token`).
  Errors come from stderr only.
- Stop with `phonecam stop` (SIGTERM, 5s, never SIGKILL). Do not kill
  `gst-launch`.
- Empty `binaryPath`: `PATH` `phonecam`, then `$HOME/.local/bin/phonecam`,
  then `$(go env GOPATH)/bin/phonecam`.
- Live preview: `bin/phonecam-preview` keeps `gst-launch` on the session
  v4l2 node (`/dev/video10` unless `session.json` says otherwise) and
  ping-pongs `frame-0.jpg` / `frame-1.jpg` so the panel shows motion, not
  one still. Runs **only while the panel is open**. Do not use Qt `Camera`
  (it falls back to the laptop webcam). Closing the panel stops the extra
  opener.

## QR

QR content is compact JSON from GET `/pairing` (`v`, `name`, `laptop_id?`,
`control`, `rtp`, `session`, `token`, `expires`, `transport`, `video`).
`bin/phonecam-qr` reads compact JSON on stdin (one line; never put the token
on argv from the service), pipes to `qrencode --type ASCII --margin 4
--level L`, and collapses ASCII pairs to a square `0/1` matrix (same idea as
`omarchy-network-qr`). After `expires`, the UI offers Restart (stop then
start, new token), not a fake Refresh.

## Doctor

Blocking Start when `phonecam doctor` reports any **FAIL** (same as CLI
exit 1). WARN/INFO (holders, firewall, LAN privacy) do not block.

## Settings

Omarchy plugin schema (`barWidget.defaults` + `schema`), not an in-panel
settings dump:

| Key | Type | Default |
|---|---|---|
| `binaryPath` | string | `""` |
| `controlPort` | integer 1024–65535 | `47470` |
| `rtpPort` | integer 1024–65535 | `47471` |

## No PhoneCam patches

This directory is UI only. Protocol, CLI, and Android stay in their trees.
