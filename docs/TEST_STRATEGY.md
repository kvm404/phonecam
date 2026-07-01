# PhoneCam Linux v0.1 Test Strategy

## Quality Bar

The release is not successful because the CLI runs. It is successful when a
Linux user can install PhoneCam, run `phonecam start`, scan a QR code, select
PhoneCam in real apps, and complete a long meeting without complicated setup.

## Primary Test Seams

1. End-to-end product seam:
   Android camera stream to Linux virtual camera to browser/meeting preview.
2. Linux diagnostics seam:
   `phonecam doctor` explains every common setup failure with useful commands.
3. CLI lifecycle seam:
   `start`, `status`, and `stop` behave predictably and clean up child
   processes.

## Performance Tests

- Measure glass-to-browser latency with a repeatable stopwatch or timestamped
  test-pattern method.
- Measure CPU usage during 720p30 streaming.
- Track dropped frames.
- Run a 90-minute stability test.
- Test on good Wi-Fi and at least one degraded Wi-Fi scenario.
- Store benchmark results in `docs/BENCHMARKS.md`.

Initial pass targets:

- under 180 ms glass-to-browser latency on good Wi-Fi,
- under 25% CPU on midrange laptop baseline,
- under 1% sustained dropped-frame rate on good Wi-Fi,
- no unrecovered stream failure over 90 minutes,
- no silent latency drift above 400 ms.

Reference baseline:

- laptop: Intel Core i5 8th generation or newer, or AMD Ryzen 5 4000-series or
  newer,
- distro/session: Arch Linux on Wayland/Hyprland first,
- network: 5 GHz Wi-Fi on the same LAN,
- Android: Android 10 or newer with hardware H.264 encode.

## Compatibility Matrix

Each release candidate should record whether PhoneCam is visible and rendering
video in:

- Chromium browser preview,
- Firefox browser preview,
- Google Meet,
- Zoom,
- Discord,
- OBS.

Each result should include:

- distro,
- kernel version,
- desktop/session,
- browser/app version,
- virtual camera format,
- observed issue or pass note.

Compatibility results should be stored in `docs/COMPATIBILITY_MATRIX.md`.

## CLI Tests

CLI tests should validate external behavior:

- command output,
- exit codes,
- config file handling,
- pairing payload shape,
- error messages,
- process lifecycle,
- child process cleanup.

## Doctor Tests

`phonecam doctor` should be tested with controlled missing/failing conditions:

- missing `v4l2loopback`,
- module installed but not loaded,
- virtual camera absent,
- virtual camera permission denied,
- missing GStreamer,
- missing GStreamer plugins,
- occupied UDP/control port,
- likely firewall block,
- unsupported or unknown distro.
- insecure LAN warning acknowledgement where applicable.

## Media Pipeline Tests

Before the Android streamer is ready, use deterministic GStreamer test sources
to validate virtual camera output. After Android streaming exists, add an
end-to-end manual and eventually automated smoke path.

## Release Gate

v0.1 should not be tagged until:

- the latency target is measured and documented,
- 90-minute stability is measured and documented,
- benchmark results are recorded in `docs/BENCHMARKS.md`,
- the app compatibility matrix is filled for at least Arch on Wayland/Hyprland,
- `phonecam doctor` covers the common failure cases,
- install docs exist for Arch, with Fedora and Ubuntu/Debian either complete or
  explicitly marked as pending.
