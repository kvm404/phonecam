# PhoneCam Compatibility Matrix

> **Status (v0.1):** This matrix is a template of the apps and platforms we want
> to verify. Results are not yet independently verified beyond the single
> reference setup (Arch Linux + a vivo Android device). Community reports for
> other distros, phones, and apps are welcome.

This file records app and platform compatibility evidence for release decisions.

## Required v0.1 Apps

- Chromium browser preview.
- Firefox browser preview.
- Google Meet.
- Zoom.
- Discord.
- OBS.

## Required Fields

Each compatibility entry should include:

- distro,
- kernel version,
- desktop/session,
- app/browser version,
- virtual camera device,
- `v4l2loopback` parameters,
- pixel format,
- pass/fail result,
- notes.

## Matrix

No formal compatibility runs have been recorded yet.

Single-setup observation (Arch Linux + vivo Android device, not a formal run):
PhoneCam appeared as a camera and streamed 720p30 smoothly in Google Meet and
Discord. Clearly labeled as a one-off reference-setup observation, not verified
across the app/distro combinations below.

| Date | App | Distro | Kernel | Session | App Version | Device | Format | Result | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |

