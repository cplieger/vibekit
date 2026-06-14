# Roadmap

Vibekit is **alpha** (currently `v0.x`). The core feature set is in place; the
focus now is hardening and polish, not new scope. This document supersedes the
[default fleet roadmap](https://github.com/cplieger/.github/blob/main/ROADMAP.md)
for this repository.

## Current focus (alpha → stable)

- **Testing.** Broaden coverage of the chat/streaming pipeline, checkpoints,
  the subagent crew, and MCP integration; raise confidence before a stable
  release. (Vibekit already has a large Go + property/fuzz suite; this is about
  closing remaining gaps surfaced below.)
- **UI.** Continued refinement of the client surfaces — editor/diff/conflict
  modes, the permissions and MCP panels, the git views, and the streaming
  render path.
- **UX.** Interaction polish: animation/easing vocabulary consistency,
  notification behaviour, loading/active states, and the overall feel of
  real-mode chat flows.

## Ongoing

- Incorporate fixes from the weekly central fuzzing and
  [gremlins](https://gremlins.dev/) mutation-testing runs.
- Adopt the stable `tsgo`
  ([@typescript/native-preview](https://www.npmjs.com/package/@typescript/native-preview))
  release for the web frontend once it reaches general availability.
- Dependency and base-image currency via Renovate; security findings
  (CodeQL / Trivy / Scorecard) addressed as they arise.
- Bug and security response per
  [SECURITY.md](https://github.com/cplieger/.github/blob/main/SECURITY.md).
