# Contributing To PhoneCam Linux

## Branching

- `main` is protected by convention and should always represent reviewed work.
- Use short-lived branches:
  - `plan/...` for planning and design docs,
  - `feat/...` for features,
  - `fix/...` for bug fixes,
  - `docs/...` for documentation-only changes,
  - `chore/...` for maintenance.

## Commits

Use Conventional Commits:

```text
feat: add pairing session model
fix: report missing gstreamer plugin
docs: add Arch install guide
test: add doctor missing-v4l2loopback case
chore: initialize repository
```

Keep commits focused. Do not mix unrelated refactors with feature work.

## Pull Requests

Every meaningful change should go through a PR before merging.

A PR should include:

- what changed,
- why it changed,
- how it was tested,
- screenshots/logs for user-facing CLI or Android UI changes,
- performance notes for media pipeline changes,
- compatibility notes when app visibility can be affected.

## Review Expectations

Review should focus on:

- correctness,
- setup failure behavior,
- latency and CPU impact,
- Linux distro compatibility,
- app compatibility,
- security/privacy implications,
- whether docs and diagnostics match the behavior.

Planning PRs should be reviewed before implementation starts. Implementation
PRs touching media transport, pairing, install behavior, or security/privacy
should receive extra scrutiny.

## Merge Policy

Do not merge until:

- review is complete,
- requested changes are resolved,
- tests or manual verification are documented,
- a maintainer has explicitly approved the change.

Merging is done by a maintainer once the PR is approved and CI is green.

