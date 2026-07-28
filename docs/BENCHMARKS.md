# PhoneCam Benchmarks

> **Status (v0.1):** The numbers below are release *targets* and this table is a
> template. They have not been independently verified beyond the single
> reference setup (Arch Linux + a vivo Android device). Treat unrecorded rows as
> unmeasured, not as passing.

This file records benchmark evidence for release decisions.

## Reference Baseline

- Laptop: Intel Core i5 8th generation or newer, or AMD Ryzen 5 4000-series or newer.
- Memory: 8 GB RAM or higher.
- Graphics: integrated graphics baseline.
- Distro/session: Arch Linux on Wayland/Hyprland first.
- Network: 5 GHz Wi-Fi on the same LAN.
- Android: Android 10 or newer with hardware H.264 encode.

## Measurement Method

Latency should be measured glass-to-browser with a repeatable stopwatch or
timestamped test-pattern method. CPU should be captured during steady-state
720p30 streaming. Stability should include a 90-minute run.

## v0.1 Target

- Resolution/FPS: 1280x720 at 30 FPS.
- Glass-to-browser latency: under 180 ms on good Wi-Fi.
- Laptop CPU: under 25% on the reference baseline.
- Dropped frames: under 1% sustained on good Wi-Fi.
- Stability: no unrecovered stream failure over 90 minutes.

## Results

No instrumented benchmark runs have been recorded yet.

One qualitative observation on the reference setup (Arch Linux + vivo Android
device, single setup, not instrumented): 720p30 streaming looked smooth in
Google Meet and Discord. This is an informal single-setup observation, not a
measured result.

| Date | Build | Laptop | Android | Network | Latency | CPU | Dropped Frames | Duration | Result | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |

