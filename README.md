# vibekit

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/size.json)](https://github.com/cplieger/vibekit/pkgs/container/vibekit)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Debian](https://img.shields.io/badge/base-Debian-A81D33?logo=debian)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/coverage.json)](https://github.com/cplieger/vibekit/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/mutation.json)](https://github.com/cplieger/vibekit/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13224/badge)](https://www.bestpractices.dev/projects/13224)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/vibekit/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/vibekit)
[![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/vibekit/releases)

A browser-based front-end for the **Kiro CLI**: chat with an AI coding agent from any device, with a live terminal, a file editor, checkpoints, and git/forge workflows in the same tab.

Vibekit runs `kiro-cli` as an Agent Client Protocol (ACP) subprocess and wraps it in a full workspace UI. The **server is the single source of truth**: every action is persisted and echoed to all connected clients over Server-Sent Events, so a conversation open on your phone and your desktop stays in sync with no drift. One `kiro-cli` bridge backs each chat and is shared by every tab and device viewing it.

Published as a multi-arch (amd64 + arm64) container image on **GHCR** (`ghcr.io/cplieger/vibekit`) and **Docker Hub** (`cplieger/vibekit`).

## ⚠️ It drives an AI agent with shell and file access

Vibekit controls an agent that can run shell commands and read and write your files under `/workspace`, and it exposes kiro-cli's stored credentials. It has **no built-in authentication** — anyone who can reach the port can use it. Before exposing it beyond your own machine, do one (ideally both) of:

- put it behind an authenticating reverse proxy (Caddy forward-auth, oauth2-proxy, Authentik, …), and/or
- keep the published port on loopback or a private network.

Signing in from the UI authenticates **kiro-cli to AWS** (the agent's identity); it is not a gate on vibekit itself.

## Run

```yaml
# compose.yaml
services:
  vibekit:
    image: ghcr.io/cplieger/vibekit:latest
    user: "1000:1000" # match your host user
    ports:
      - "9847:9847"
    volumes:
      - ./config:/config # chats, kiro-cli auth/state, tools
      - ./workspace:/workspace # your repos
    restart: unless-stopped
```

Before the first start, create and own the bind-mount directories, since the container runs as `user: "1000:1000"`. The entrypoint does not `chown` them, so a root-owned host directory makes first boot fail with `failed to create config directories`:

```bash
mkdir -p /opt/appdata/vibekit/config /opt/appdata/vibekit/workspace
chown -R 1000:1000 /opt/appdata/vibekit
```

To skip managing host ownership, run as root instead with `user: "0:0"` (less secure).

Open <http://localhost:9847>. `kiro-cli` is downloaded and pinned on first boot (it is not redistributed in the image, per the AWS Customer Agreement). On first launch, sign in to `kiro-cli` and start chatting.

## Capabilities

Vibekit is a full workspace in the browser, not just a chat box. Everything below is reachable from any device viewing the same server.

**Chat**, from any device and always in sync:

- Multi-device conversations kept in sync live over SSE; full history, one file per chat.
- Streaming markdown responses with collapsible reasoning ("thinking") blocks.
- Prompt queue: keep typing while the agent works; queued prompts drain in order as turns finish.
- Inline shell: prefix a message with `!` to run a command locally.
- Slash commands (`/compact`, …) with type-ahead completion.
- File attachments by drag-drop or paperclip; PDF/CSV/Office documents are sent to the agent as documents.
- Find-in-chat (Ctrl-F) and per-chat export to Markdown or JSON.
- On-demand and automatic context compaction; configurable chat retention with an archive/History view.

**Agent control** over models, modes, and turns:

- Switch modes/roles — bundled workflow modes (Default, Spec, Quick Spec, Bug Fix, Plan, Autonomous) and your own workspace agents from `.kiro/agents/`.
- Switch models on the fly, with per-model credit multipliers.
- Set reasoning effort (low through max).
- Approve or deny tool-call permission prompts; answer MCP input (elicitation) forms.
- Plan handoff: edit a proposed plan in the editor, or run it directly.
- Rewind: branch a new conversation from any past turn, then merge it back or discard it.
- Subagents and agent-spawned terminals render as nested, collapsible cards.

**Editing and files** in the browser:

- File browser: navigate, rename, delete, download, and upload (dialog or drag-drop).
- Syntax-highlighted editor with edit/save and deep links to a line (`#L<n>`).
- Diff view against the last save or against git `HEAD`.
- Merge-conflict resolver — accept ours/theirs/both per hunk, with an AI "suggest" merge.
- Gutter markers on the lines the agent changed.

**A real terminal** in the browser:

- A PTY shell (via [web-terminal-engine](https://github.com/cplieger/web-terminal-engine)) that survives sleep and network drops, with a mobile key toolbar.

**Version control and forges** for GitHub, GitLab, Codeberg, and Gitea/Forgejo:

- Git panel: stage, commit (with an AI-written message), diff, and switch branches.
- Pull requests: list, create (with an AI-written description), merge, and close.
- Connect forge accounts via OAuth device flow or a token (driven by the `gh` / `glab` / `tea` CLIs).

**Safety nets** around every agent change:

- Checkpoints: content-addressed per-file snapshots, independent of git — restore everything or undo a single file, with cross-chat conflict detection and click-to-diff.
- Supervised mode: stage every agent write for per-hunk accept/reject/merge before it touches disk; grant trust for the rest of a turn when you're confident.
- Permissions: a Cedar policy editor (allow/deny/ask per capability, with path scoping), shell-command tiers, and a "test a decision" explainer.

**MCP** server management:

- Add, edit, and remove MCP servers (local, or remote over HTTP/SSE) from the UI.
- OAuth (device flow, client id/secret), per-server auto-approve, live reconnect, and prompt/resource browsing.

**Workspace tools** and agent configuration:

- Manage installed tools — search a catalog of ~700 runtimes, language servers, and CLIs (compiled from the mise and aqua registries), install them in the background with streamed progress, pin versions, or bring your own install command.
- Knowledge bases: index workspace directories, with live progress.
- Spec board (`/specs`): a live requirements → design → tasks tree.
- Hooks: list, enable/disable, and run agent hooks; create one from chat context.
- Edit global custom instructions and browse steering docs, skills, and agents.

**Notifications** and PWA install:

- Installable as a PWA, with a home-screen icon and a share target.
- Web-push notifications when a turn finishes or the agent needs permission — even with the tab closed.

**Settings** and per-device preferences:

- Per-device layout (tabs, open files, shell) with light and dark themes.
- Account/subscription usage, a per-session context/credit meter, build versions, and a copyable diagnostics report.

## Configuration

The image ships working defaults; most setups only choose the volumes and how to expose the port.

- **Port:** `9847` (HTTP + SSE + the shell WebSocket).
- **Volumes:** `/config` persists chats, kiro-cli auth/state, installed tools, and settings; `/workspace` is your repositories.
- **User:** the compose above runs as `1000:1000` (see the first-boot ownership note). Run as root (`user: "0:0"`) to skip managing host ownership.
- **Health:** `GET /api/health` reports healthy once the server is up.

### Behind a reverse proxy (`TRUSTED_PROXIES`)

The access log and the login/logout audit logs record a `client_ip`. How that address is resolved depends on how you expose vibekit:

- **Directly exposed** (no reverse proxy in front): leave `TRUSTED_PROXIES` unset. `client_ip` is the address of the connecting socket, which cannot be forged at this layer. Any `X-Forwarded-For` header a client sends is ignored.
- **Behind a reverse proxy**: set `TRUSTED_PROXIES` to your proxy's address range so `client_ip` shows the real client instead of the proxy's own address. It is a comma-separated list of CIDRs (bare IPs are accepted as single hosts), for example:

  ```yaml
  environment:
    TRUSTED_PROXIES: "10.0.0.0/8,192.168.0.0/16"
  ```

`X-Forwarded-For` is honored **only** when the connecting peer falls inside `TRUSTED_PROXIES`; otherwise the header is ignored and the socket peer is logged. List every proxy hop between the client and vibekit. This is spoof-safe by default: an empty, unset, or misconfigured value falls back to the unspoofable socket peer rather than trusting a client-supplied header.

## How it fits together

```text
Browser (any device)          vibekit server (Go)            kiro-cli (ACP)
--------------------          -------------------            --------------
POST /api/command        →    persist + dispatch        →    one bridge per chat
GET  /api/events (SSE)   ←    broadcast to all clients  ←    stream updates
```

The server owns all state and writes one JSON file per chat; clients render only what the server has confirmed. That is what keeps every device — phone, tablet, laptop — showing the same conversation with no optimistic local state and no multi-device drift.

## Security

- **No built-in authentication** — put vibekit behind an authenticating reverse proxy and/or a private network (see the warning above).
- Web push uses an SSRF-hardened transport.
- Debian base: a shell and the `kiro-cli` subprocess are required, so this is intentionally not distroless.
- Images are published with cosign signatures and SBOM attestations.

## kiro-cli

`kiro-cli` is downloaded and pinned on first boot rather than baked into the image (the AWS Customer Agreement governs redistribution, so you accept it by booting the container). Upgrades arrive by pulling a newer image tag; there is no in-place self-update.

## Related projects

- [web-terminal-kiro](https://github.com/cplieger/web-terminal-kiro): the sister app — a raw browser terminal that drives kiro-cli's own TUI, instead of this chat-first UI.
- [web-terminal-engine](https://github.com/cplieger/web-terminal-engine): the terminal engine (Go PTY/VT + TypeScript renderer) behind vibekit's shell.
- [actions](https://github.com/cplieger/actions): the client-side action framework vibekit's UI is built on.

## Contributing

Architecture, the invariants you must not break, and local build/test instructions are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0. See [LICENSE](LICENSE).
