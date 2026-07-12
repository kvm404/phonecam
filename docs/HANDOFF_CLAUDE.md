# Claude Code Handoff

## Repository

- Repo: `kvm404/phonecam`
- Visibility: private
- Local path: `/home/krvm/Projects/phonecam`
- Default branch: `main`
- Current working branch at handoff: `feat/gstreamer-receiver-foundation`
- Open PR: https://github.com/kvm404/phonecam/pull/14

## Workflow Rules

- Use short-lived branches and PRs.
- Do not merge without explicit user approval.
- Keep commits focused and use Conventional Commits.
- Run Linux CLI tests before opening/updating PRs:

```sh
cd linux-cli
mise x go@1.22 -- go test ./...
mise x go@1.22 -- go test -race ./...
```

- For GStreamer-related work, also validate required elements when available:

```sh
for e in udpsrc rtpjitterbuffer rtph264depay h264parse avdec_h264 videoconvert v4l2sink; do
  gst-inspect-1.0 "$e" >/dev/null || exit 1
done
```

## Product Direction

v0.1 architecture is:

```text
Android CameraX
  -> MediaCodec H.264
  -> RTP/UDP over LAN
  -> Linux GStreamer receiver
  -> v4l2loopback /dev/video10
  -> meeting apps / OBS / browsers
```

Target:

- 720p30
- under 180 ms glass-to-browser latency on good Wi-Fi
- under 25% laptop CPU on a midrange Linux machine
- stable 90-minute sessions
- visible in Meet, Zoom, Discord, OBS, Chromium, and Firefox

## Completed Work

Merged:

- PR #1: v0.1 PRD, technical design, test strategy, roadmap, workflow docs.
- PR #4: Linux CLI foundation and `phonecam doctor`.
- PR #6: pairing session foundation.
- PR #8: control server foundation.
- PR #10: `phonecam start` foundation.
- PR #12: terminal QR rendering.

Open:

- PR #14: GStreamer receiver foundation.

## Current Open PR

PR #14 adds:

- `linux-cli/internal/gstreamer`
- structured pipeline config and validation
- `gst-launch-1.0` argv builder for:

```text
udpsrc
  ! rtpjitterbuffer drop-on-latency=true
  ! rtph264depay
  ! h264parse
  ! avdec_h264
  ! videoconvert
  ! video/x-raw,format=YUY2,width=1280,height=720,framerate=30/1
  ! v4l2sink device=/dev/video10 sync=false
```

- runner that launches `gst-launch-1.0`, passes context cancellation, and captures stdout/stderr on failure.

Validation already performed:

- `go test ./...`
- `go test -race ./...`
- local `gst-inspect-1.0` checks for required elements
- GitHub Actions passed

The PR is open, CI is green, and review is required. Do not merge unless the user explicitly approves.

## High-Priority Remaining Work

1. Wire GStreamer into `phonecam start`.
   - Start receiver after pairing session creation or after phone approval.
   - Use the RTP port already allocated by start runtime.
   - Ensure process is cancelled on shutdown.
   - Surface GStreamer failures clearly.

2. Add v4l2loopback lifecycle helpers.
   - Detect `/dev/video10`.
   - Verify `/sys/class/video4linux/video10/name == PhoneCam`.
   - Check write permissions.
   - Later: optionally help load `v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1`.

3. Implement `phonecam status` and `phonecam stop`.
   - Current `start` is foreground-only.
   - A later background/session model is needed for real `status` and `stop`.

4. Add an RTP/GStreamer smoke mode.
   - Before Android exists, use a deterministic GStreamer sender/test source.
   - Goal: prove frames can reach v4l2loopback locally.

5. Start Android Kotlin app.
   - Tell the user before starting Android work so they can plug in their phone.
   - App should be native Kotlin, not React Native.
   - The user mentioned `~/Projects/railreel` as a possible reference for local workflow only.

## Android Work Still Needed

- Gradle project scaffold.
- CameraX preview.
- QR scanner.
- Control API client:
  - scan payload,
  - call `/pair`,
  - wait for approval/status.
- MediaCodec H.264 encoder.
- RFC 6184 RTP packetizer:
  - packetization mode 1,
  - FU-A fragmentation,
  - SPS/PPS before IDR,
  - 90 kHz RTP timestamps,
  - random sequence and SSRC.
- RTP/UDP sender.
- battery/thermal status reporting.

## Important Security Notes

- RTP is not encrypted in the first implementation spike.
- Current privacy posture is local-network-only, no account, no cloud relay.
- QR token is short-lived and session-scoped.
- `/pairing` and `/approve` are loopback-only.
- Android calls `/pair`; Linux-side local approval calls `/approve`.
- RTP source must match approved control IP/port/SSRC before being accepted.

## Useful Files

- `docs/PRD.md`
- `docs/TECHNICAL_DESIGN.md`
- `docs/TEST_STRATEGY.md`
- `docs/adr/0001-mvp-media-transport.md`
- `linux-cli/internal/cli`
- `linux-cli/internal/doctor`
- `linux-cli/internal/pairing`
- `linux-cli/internal/control`
- `linux-cli/internal/start`
- `linux-cli/internal/qrcode`
- `linux-cli/internal/gstreamer`

## Suggested Next PR

Suggested branch:

```sh
git switch main
git pull --ff-only
git switch -c feat/start-gstreamer-receiver
```

Suggested scope:

- Merge PR #14 first if approved.
- Add GStreamer receiver lifecycle to `internal/start`.
- Start receiver with the allocated RTP port and `/dev/video10`.
- Keep `phonecam start` foreground-only.
- Cancel GStreamer process on Ctrl+C/SIGTERM.
- Add tests with injected/fake receiver runner.

Do not start Android work until the user has been told explicitly.

