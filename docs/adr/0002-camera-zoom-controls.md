# ADR 0002: Camera Zoom Controls On The Live Screen

## Status

Accepted. This supersedes the "Torch and other advanced camera controls"
non-goal in [`docs/TECHNICAL_DESIGN.md`](../TECHNICAL_DESIGN.md) for camera
zoom only. Torch remains cancelled.

## Context

The phone sits on a stand across the desk. Framing gets set once, in a hurry,
right before a meeting — and then the shot drifts: the user leans, the stand
gets nudged, a whiteboard or a second person needs to fit. Walking back to the
phone to reframe between meetings is friction the app can remove.

Camera controls as a category were a declared non-goal, torch first among
them. The user overturned that for zoom specifically: framing is the one
camera interaction this product actually needs. Torch stays cancelled.

## Decision

The LIVE screen gains zoom in, zoom out, and reset controls:

- 0.25x per step, with a live readout ("1x", "1.25x", "1.5x", "2x").
- Zoom is applied through the CameraX zoom ratio
  (`Camera.getCameraControl().setZoomRatio()`), i.e. sensor-crop zoom. The
  capture resolution, encoder, and stream quality are unchanged — the lens
  crops in and still delivers the same 720p30 frame.
- Ratios are clamped to the camera's device-reported range
  (`ZoomState.minZoomRatio` / `maxZoomRatio`); buttons disable at the bounds.
- The zoom row is hidden entirely when the active lens has no zoom range.
- Zoom resets to 1x when a stream starts and when the camera flips.
  Re-attaching the preview to the already-streaming lens keeps the user's
  zoom.

## Alternatives Considered

- Software crop in `FrameConverter`: rejected. Cropping the frames discards
  pixels and lowers effective quality — the encoder would encode a smaller
  image instead of a tighter view — and it puts zoom on the CPU hot path.
  CameraX sensor-crop zoom does it in hardware for free.
- Desktop-side remote zoom via the status-poll command channel: deferred as a
  v2, not rejected. The command channel already exists, so the laptop could
  drive the phone's zoom later. On-phone controls cover the common case
  first.

## Consequences

Benefits:

- Framing happens from the LIVE screen instead of a walk back to the phone.
- Sensor-crop zoom leaves the media pipeline untouched: no new stream
  parameters, no renegotiation.
- The controls follow whatever the device reports, so every lens behaves —
  ultra-wide below 1x, front lenses capped just past 1x, periscope lenses far
  above.

Tradeoffs:

- `StreamingService` now retains the `Camera` handle from `bindToLifecycle`
  (previously discarded) plus a `ZoomState` observer, and clears both on
  teardown.
- Zoom behavior varies per device and lens; the app renders the camera's real
  range rather than a normalized curve.
- Per-lens zoom curves via Camera2 interop remain out of scope.

## Follow-Up

- Revisit desktop-side remote zoom over the status-poll command channel if
  desk-side framing becomes a real need.
