# ADR 0001: Use RTP/UDP + H.264 + GStreamer For v0.1

## Status

Accepted.

## Context

PhoneCam v0.1 needs a low-latency local-network Android-to-Linux webcam path.
The performance target is 720p30, under 180 ms glass-to-browser latency on good
Wi-Fi, under 25% CPU on a midrange Linux machine, and stable 90-minute sessions.

The first design considered WebRTC, but the MVP does not require NAT traversal,
remote networking, browser-native phone capture, or TURN relay.

## Decision

v0.1 will use:

- CameraX for Android camera capture,
- MediaCodec hardware H.264 encode,
- RTP/UDP for local video transport,
- GStreamer for Linux RTP receive/decode/convert/output,
- `v4l2loopback` for virtual webcam exposure,
- Go for Linux CLI/control orchestration.

Go will not decode video frames in the v0.1 hot path.

The first v0.1 implementation spike will use unencrypted RTP on the local LAN
with token-bound pairing, source IP/port pinning, SSRC validation, and explicit
LAN privacy disclosure. SRTP or another encrypted transport remains a public
release decision.

## Consequences

Benefits:

- Lower MVP complexity than WebRTC.
- Good fit for LAN-only low-latency streaming.
- Android hardware encode is widely available.
- GStreamer provides mature RTP/H.264 and V4L2 integration.
- The CLI can focus on setup, pairing, diagnostics, and lifecycle.

Tradeoffs:

- RTP/UDP is not encrypted by default.
- NAT traversal and remote-network use are not handled.
- GStreamer dependency quality varies by distro.
- Hardware decode support needs careful detection before default use.

## Follow-Up

- Decide whether public v0.1 adds SRTP or ships with explicit LAN encryption
  disclosure.
- Validate final virtual camera pixel format through app compatibility testing.
- Revisit WebRTC only if remote networking or encrypted media transport becomes
  a product requirement.
