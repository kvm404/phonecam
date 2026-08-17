# PhoneCam Linux v0.1 PRD

## Problem Statement

Linux users often have poor built-in webcams, unreliable support from
commercial phone-webcam tools, or privacy concerns with account/cloud-based
camera products. Wayland and Hyprland users also hit Linux-specific setup pain:
kernel modules, virtual camera visibility, media pipeline dependencies, app
compatibility, and unclear diagnostics.

Users need a Linux-first way to turn an Android phone camera into a reliable
virtual webcam for meetings, streaming, and recording without accounts, cloud
video processing, or complicated setup.

## Solution

PhoneCam Linux provides a local-network Android-to-Linux camera pipeline. The
Android app captures camera video, encodes it with hardware H.264, and streams
it over RTP/UDP to the Linux CLI. The Linux CLI manages pairing, diagnostics,
virtual camera setup, and GStreamer process lifecycle. GStreamer decodes the
stream and writes frames into a `v4l2loopback` virtual webcam that meeting apps
can select as `PhoneCam`.

The v0.1 success bar is:

- 720p30 video.
- Under 180 ms glass-to-browser latency on good Wi-Fi.
- Under 25% CPU on a midrange Linux laptop.
- Stable for 90 minutes.
- Visible in Google Meet, Zoom, Discord, OBS, Chromium, and Firefox.
- Clear `phonecam doctor` output for every common failure.

## User Stories

1. As a Linux user, I want to use my Android phone camera as a webcam, so that I get better video quality than my laptop webcam.
2. As a student, I want setup to take only a few commands, so that I can join classes without fighting Linux media configuration.
3. As a Hyprland user, I want a CLI-first tool, so that it fits my Wayland desktop workflow.
4. As a privacy-conscious user, I want no account requirement, so that I can use the tool without sharing identity data.
5. As a privacy-conscious user, I want local video streaming by default, so that my camera feed is not sent through cloud infrastructure.
6. As a meeting participant, I want the phone camera to appear as a normal webcam, so that Google Meet, Zoom, Discord, and browsers work without plugins.
7. As an OBS user, I want PhoneCam to appear as a standard V4L2 device, so that I can add it as a video capture source.
8. As a first-time user, I want `phonecam start` to show a QR code, so that pairing the phone is fast and unambiguous.
9. As an Android user, I want to scan the QR code from the app, so that I do not manually type IP addresses or ports.
10. As a user on shared Wi-Fi, I want the QR token to expire quickly, so that stale pairing codes cannot be reused.
11. As a user, I want device approval on both sides, so that unexpected phones cannot silently connect.
12. As a Linux user, I want `phonecam doctor` to explain missing dependencies, so that I know exactly what to install.
13. As a Linux user, I want `phonecam doctor` to validate `v4l2loopback`, so that webcam apps can see the virtual device.
14. As a Linux user, I want `phonecam doctor` to check app visibility, so that I can diagnose why a meeting app does not show PhoneCam.
15. As a Wayland user, I want desktop-specific notes when relevant, so that I understand PipeWire, browser, and sandbox limitations.
16. As an Arch user, I want Arch-first docs, so that I can install kernel modules and media dependencies correctly.
17. As a Fedora user, I want Fedora docs, so that I can handle package names, kernel modules, and firewall defaults.
18. As an Ubuntu/Debian user, I want distro docs, so that I can install dependencies without guessing package names.
19. As a user with weak Wi-Fi, I want late frames dropped instead of buffered indefinitely, so that video stays live instead of drifting behind.
20. As a meeting participant, I want stable long sessions, so that the camera does not fail during a 90-minute call.
21. As an Android user, I want a preview before streaming, so that I can frame the camera correctly.
22. As an Android user, I want a clear Start/Stop control, so that I control when video is transmitted.
23. As an Android user, I want battery and thermal status, so that I can react before long meetings degrade.
24. As a Linux user, I want `phonecam status`, so that I can see whether the receiver, stream, and virtual camera are healthy.
25. As a Linux user, I want `phonecam stop`, so that I can reliably shut down the receiver and media pipeline.
26. As a Linux user, I want optional systemd user service support later, so that PhoneCam can run in the background.
27. As a developer, I want media hot paths delegated to GStreamer, so that the Go CLI stays focused on product behavior.
28. As a maintainer, I want measurable latency and stability targets, so that regressions are caught before release.
29. As a maintainer, I want a compatibility matrix, so that claims about Meet, Zoom, Discord, OBS, and browsers are verified.
30. As a future contributor, I want clear architecture docs, so that new features do not accidentally compromise latency or compatibility.

## Implementation Decisions

- The v0.1 transport is RTP/UDP over local network.
- The v0.1 video codec is Android hardware H.264 via MediaCodec.
- The Android capture layer uses CameraX by default, with Camera2 only where
  CameraX cannot expose required low-level behavior.
- The Linux CLI is written in Go.
- The Linux CLI orchestrates media processes; it does not decode or transform
  video frames directly in v0.1.
- GStreamer owns RTP reception, depayloading, H.264 parsing, decoding,
  color conversion, and writing to the virtual camera.
- `v4l2loopback` is the virtual webcam backend for Linux.
- The default target is 1280x720 at 30 FPS.
- The default latency policy is to drop late frames and keep video live.
- The QR pairing payload contains laptop address, control port, RTP port,
  short-lived pairing token, session ID, expiry, transport mode, and protocol
  version.
- The v0.1 media privacy posture is local-network-only with explicit LAN threat
  disclosure, token-bound pairing, source pinning, SSRC validation, and packet
  rejection before approval. RTP payload encryption is not part of the first
  implementation spike.
- Local video relay through the cloud is out of scope for v0.1.
- Optional STUN/WebRTC-style traversal is out of scope for v0.1.
- Audio is out of scope for v0.1.
- USB transport is out of scope for v0.1 and remains out of scope until v1.0.
- Multi-phone support is out of scope for v0.1.
- Persistent trusted-device pairing is out of scope for v0.1 and is a v0.2
  goal; see [`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md).

## Testing Decisions

- The highest-value test seam is the external product seam: Android stream to
  Linux virtual camera to browser/meeting app preview.
- The second critical seam is `phonecam doctor`, because Linux setup failures
  are expected to be the main source of user pain.
- CLI tests should validate external behavior: command output, exit codes,
  process lifecycle, config handling, and generated pairing payloads.
- Media pipeline tests should use a deterministic test source before real
  Android streaming is available.
- Compatibility tests should record whether PhoneCam appears and renders video
  in Chromium, Firefox, Google Meet, Zoom, Discord, and OBS.
- Performance tests should measure glass-to-browser latency, CPU usage, dropped
  frames, reconnect behavior, and 90-minute stability.
- Benchmark and compatibility results should be recorded in project docs so the
  release gate is auditable.
- Tests should prefer black-box behavior over internal implementation details.

## Out of Scope

- iOS support.
- Phone microphone/audio.
- USB transport (until v1.0).
- Polished GUI tray.
- Multi-phone support.
- Advanced camera controls (torch stays Later; front/back flip and the
  720p/540p/360p selector shipped in v0.1).
- Cloud TURN relay.
- Remote-network usage.
- Browser-native phone capture.
- Production distro packaging.

## Further Notes

The MVP should prioritize a boring, reliable, measurable camera path over a
large feature set. The first implementation milestone is an end-to-end latency
spike proving Android H.264 RTP can reach a Linux `v4l2loopback` device and
appear in a browser preview within the target latency budget.
