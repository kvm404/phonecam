# PhoneCam for Omarchy

Bar widget that operates the PhoneCam CLI: start/stop, pairing QR, status,
doctor, and trusted phones. It is a loopback HTTP client. It does not bind
ports and does not change the PhoneCam protocol.

Do **not** `omarchy plugin add` the PhoneCam git URL. That command validates
the clone root, and this repo’s root has no `manifest.json`.

```sh
./omarchy-plugin/install.sh
omarchy plugin enable io.github.kvm404.phonecam
```

`install.sh` copies into `~/.config/omarchy/plugins/io.github.kvm404.phonecam`
and prints the enable command. It does not enable the plugin itself.

While the stream is live, the open panel snapshots `/dev/video10` (PhoneCam),
not the laptop webcam. Closing the panel stops those snapshots.

Needs `phonecam` on PATH (or `~/.local/bin/phonecam`), `curl`, `setpriv`,
`qrencode`, and Omarchy 4+ with Quickshell. Press Start in the panel; the
plugin does not auto-start at login. Scan the QR with the PhoneCam Android
app. The LAN RTP stream is unencrypted.

Settings (Omarchy plugin schema): `binaryPath`, `controlPort` (default
47470), `rtpPort` (default 47471).

Design: [`docs/omarchy-plugin.md`](../docs/omarchy-plugin.md).
