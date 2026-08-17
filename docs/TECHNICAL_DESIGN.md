# PhoneCam Linux v0.1 Technical Design

This document is the v0.1 architecture. v0.2 scope and implementation
contracts live in [`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md)
and [`docs/ROADMAP.md`](ROADMAP.md). USB transport stays a non-goal until v1.0.
Torch and a latency measurement harness are unscheduled (Later), not v0.2.

## Goals

- Deliver a low-latency Android-to-Linux virtual webcam.
- Keep media hot paths inside proven platform/media components.
- Make Linux setup failures visible and actionable.
- Keep the system local-first and private by default.

## Non-Goals

- Audio transport.
- USB transport.
- Remote-network traversal.
- WebRTC signaling.
- GUI tray.
- Multi-camera or multi-phone sessions.
- Advanced camera controls.

## Architecture

```text
+-------------------+       RTP/UDP        +-------------------------+
| Android app        | -------------------> | Linux GStreamer pipeline |
| CameraX + H.264    |                      | RTP/H.264 -> raw video  |
+---------+---------+                      +------------+------------+
          |                                             |
          | HTTP/WebSocket control                      | v4l2sink
          v                                             v
+-------------------+                      +-------------------------+
| Linux Go CLI       |                      | v4l2loopback device     |
| pairing/lifecycle  |                      | /dev/video10 PhoneCam   |
+-------------------+                      +-------------------------+
```

## Component Responsibilities

### Android App

- Scan QR pairing payload.
- Connect to the Linux control server.
- Present camera preview.
- Start and stop streaming.
- Capture 720p30 frames with CameraX.
- Encode H.264 using MediaCodec hardware encode.
- Stream H.264 over RTP/UDP to the Linux receiver.
- Send health metadata over the control channel:
  - battery level,
  - charging state,
  - thermal status,
  - selected camera,
  - current resolution/FPS,
  - encoder bitrate,
  - dropped frame counters when available.

### Android H.264/RTP Sender

The Android sender should use CameraX with a MediaCodec encoder surface where
possible, avoiding CPU readback of camera frames in the hot path. The first
implementation should target:

- H.264 constrained baseline or baseline profile where available.
- 1280x720 at 30 FPS.
- No B-frames.
- Initial bitrate of 4 Mbps, configurable in the 3-6 Mbps range.
- IDR/keyframe interval of 1 second.
- Low-latency encoder options when supported by the device.
- RTP H.264 packetization using RFC 6184 packetization mode 1.
- FU-A fragmentation for NAL units larger than the chosen MTU.
- MTU target of 1200 bytes to avoid common Wi-Fi fragmentation problems.
- RTP clock rate of 90 kHz.
- Random initial sequence number.
- Random SSRC generated per approved session.
- SPS/PPS sent at stream start and repeated before each IDR frame.
- Sender timestamps derived from capture/encoder presentation timestamps.

The control channel announces stream parameters before RTP is accepted as
connected. If the encoder cannot provide the requested 720p30 H.264 profile, the
phone reports capabilities and the Linux side either chooses a compatible 720p30
profile or fails with a clear reason.

### Linux CLI

- Own user-facing commands.
- Create and validate pairing sessions.
- Render QR codes.
- Check dependencies and explain remediation.
- Validate or create the `v4l2loopback` device where possible.
- Launch and supervise the GStreamer receiver.
- Track status and expose clear command output.
- Stop child processes cleanly.
- Persist minimal local config only when required.

The Go CLI must not own the video decode path in v0.1.

### GStreamer Pipeline

The initial candidate pipeline is:

```text
udpsrc port=<rtp-port> caps="application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000"
  ! rtpjitterbuffer latency=<low-ms> drop-on-latency=true
  ! rtph264depay
  ! h264parse
  ! avdec_h264
  ! videoconvert
  ! video/x-raw,format=YUY2,width=1280,height=720,framerate=30/1
  ! v4l2sink device=/dev/video10 sync=false
```

Hardware decode can be added behind capability detection. The first reliable
implementation should prefer a predictable software decode fallback over
fragile hardware-specific behavior.

GStreamer names the packed 4:2:2 format as `YUY2`; this corresponds to the
common V4L2 `YUYV` format discussed in compatibility notes.

### v4l2loopback Contract

The default virtual camera target is:

```text
/dev/video10
card_label=PhoneCam
exclusive_caps=1
```

`exclusive_caps=1` is required for better browser and meeting-app visibility.
The CLI should prefer a stable `video_nr=10` when available, but it must detect
conflicts and either select another device number or explain the conflict.

The compatibility-first pixel format is YUYV/YUY2 at 1280x720 30 FPS. Additional
formats such as MJPEG may be considered only after the compatibility matrix
shows a clear need.

Permission handling should be explicit:

- detect whether the current user can write to the selected video device,
- explain `video` group or udev remediation per distro,
- never silently run the long-lived receiver as root.

## Pairing Protocol

`phonecam start` creates a short-lived pairing session and displays a QR code.

The QR payload should be versioned JSON or a compact URI containing:

- protocol version,
- laptop display name,
- control server URL,
- RTP host and port,
- session ID,
- pairing token,
- expiry timestamp,
- transport mode,
- requested video profile.

Example shape:

```json
{
  "v": 1,
  "name": "phonecam-linux",
  "control": "http://192.168.1.42:49321",
  "rtp": "192.168.1.42:49322",
  "session": "01J...",
  "token": "base64url...",
  "expires": "2026-07-01T10:00:00Z",
  "transport": "rtp-h264",
  "video": {
    "width": 1280,
    "height": 720,
    "fps": 30
  }
}
```

The token expires quickly. A phone must connect over the control channel and be
approved before the Linux receiver accepts stream state as connected.

### Pairing And Stream Binding

The v0.1 pairing model is session-scoped:

- The Linux CLI creates at least 128 bits of random token entropy.
- The token expires after a short window, initially 2 minutes.
- The Android app presents the scanned laptop identity before streaming.
- The Linux CLI shows the connecting device and requires approval before
  treating the stream as connected.
- RTP packets received before approval are ignored.
- After approval, the Linux side pins the approved source IP/port and SSRC.
- Packets from other sources or SSRCs are dropped.

v0.1 code implements `BindRTPSource` / `ValidateRTPSource` and tests them, but
the live receiver never calls them: after approval, `udpsrc` accepts any UDP on
the RTP port. Enforcing that pin is a v0.2 goal (userspace UDP gate). See
[`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md).
- Reuse of an expired or consumed token is rejected.
- `phonecam stop` invalidates the session token.

This does not make RTP encrypted. It prevents accidental or trivial injection
into an approved session and makes the LAN privacy boundary explicit.

## CLI Contract

### `phonecam start`

- Ensures or requests a virtual camera device.
- Starts the control server.
- Allocates an RTP UDP port.
- Prints virtual device path and status.
- Renders the QR code.
- Starts the GStreamer pipeline in waiting mode or starts it after phone
  approval, depending on implementation constraints.

### `phonecam status`

- Reports control server state.
- Reports connected phone identity where available.
- Reports virtual camera path.
- Reports stream resolution/FPS.
- Reports GStreamer process health.
- Reports recent error summary.

### `phonecam stop`

- Stops streaming session.
- Stops GStreamer process.
- Stops control server.
- Leaves the virtual camera device in a clean idle state.

### `phonecam doctor`

Checks:

- `v4l2loopback` installed.
- `v4l2loopback` module loadable.
- virtual device exists.
- user can write to the virtual camera.
- GStreamer installed.
- required GStreamer plugins installed.
- firewall likely permits local UDP/control ports.
- current desktop session notes for Wayland/X11.
- browser/app visibility guidance.
- distro-specific install hints.

### `phonecam install`

MVP install behavior may be documentation-first or helper-script backed. It
should identify the distro and print/install packages for:

- `v4l2loopback`,
- GStreamer core/tools/plugins,
- QR rendering dependency if external,
- kernel headers where needed.

## Latency Policy

The product optimizes for live video. It should drop late frames instead of
building large buffers. Silent drift to high latency is a failure.

Initial targets:

- glass-to-browser latency: under 180 ms on good Wi-Fi,
- hard warning threshold: 250 ms,
- failure threshold: sustained 400 ms or stream instability.

## Security And Privacy

- No account required.
- No default cloud video relay.
- Local network transport by default.
- Short-lived QR pairing token.
- Session-scoped approval.
- RTP source IP/port pinning after approval.
- RTP SSRC validation after approval.
- Packet rejection before approval.
- No persistent trusted devices in v0.1. Persistent trusted pairing is a v0.2
  goal; see [`docs/v0.2-reliability-and-controls.md`](v0.2-reliability-and-controls.md).
- Document LAN threat model clearly.

RTP/UDP is not encrypted by default in the first v0.1 implementation spike. The
product privacy claim for v0.1 is no account, no cloud relay, and local-only
streaming by default. The docs and CLI must clearly state that users on the same
LAN may be able to observe unencrypted video traffic unless a future encrypted
transport is enabled.

## Failure Behavior

- Missing dependency: `doctor` explains package names and next command.
- Missing virtual camera: show distro-specific `v4l2loopback` guidance.
- Phone disconnect: mark status disconnected and keep virtual camera available.
- GStreamer crash: capture last stderr lines and report actionable error.
- High latency: warn and prefer frame dropping over added buffering.
- Unsupported Android encoder profile: fall back within 720p30 H.264 options.

## Performance Baseline

The initial benchmark baseline should be a midrange laptop class, not a
workstation:

- Intel Core i5 8th generation or newer, or AMD Ryzen 5 4000-series or newer.
- Integrated graphics.
- 8 GB RAM.
- Recent Arch Linux on Wayland/Hyprland for the first full matrix pass.
- 5 GHz Wi-Fi on the same LAN as the phone.
- Android 10 or newer phone with hardware H.264 encode.

Software decode is the first compatibility fallback. If software decode exceeds
the 25% CPU target on the baseline, hardware decode detection becomes a v0.1
release blocker rather than a future optimization.

## Open Questions

- Whether public v0.1 should add SRTP or ship with explicit local-network
  unencrypted RTP disclosure.
- Whether hardware decode should be default-on or opt-in after compatibility
  testing.
- Final license choice before public open-source release.
