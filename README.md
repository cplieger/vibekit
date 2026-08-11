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

Vibekit controls an agent that can run shell commands and read and write your files under `/workspace`, and it exposes kiro-cli's stored credentials. It has **no built-in authentication**: anyone who can reach the port can use it. Before exposing it beyond your own machine, do one (ideally both) of:

- put it behind an authenticating reverse proxy (Caddy forward-auth, oauth2-proxy, Authentik, …), and/or
- keep the published port on loopback or a private network.

Signing in from the UI authenticates **kiro-cli to AWS** (the agent's identity); it is not a gate on vibekit itself.

## Run

```yaml
# compose.yaml
services:
  vibekit:
    image: ghcr.io/cplieger/vibekit:latest
    # Override with PUID/PGID in .env; defaults to 1000:1000.
    user: "${PUID:-1000}:${PGID:-1000}"  # match your host user
    ports:
      - "9847:9847"
    volumes:
      - ./config:/config # chats, kiro-cli auth/state, tools
      - ./workspace:/workspace # your repos
    restart: unless-stopped
```

Before the first start, create and own the bind-mount directories. The container runs as the UID from `user:` (1000 unless you set `PUID`/`PGID`), and the entrypoint does not `chown` them, so a root-owned host directory makes first boot fail with `failed to create required directories`:

```bash
mkdir -p /opt/appdata/vibekit/config /opt/appdata/vibekit/workspace
chown -R "${PUID:-1000}:${PGID:-1000}" /opt/appdata/vibekit
```

To skip managing host ownership, run as root instead with `user: "0:0"` (less secure).

Open <http://localhost:9847>. The UI comes up right away; `kiro-cli` is downloaded and verified in the background on first boot (it is not redistributed in the image, per the AWS Customer Agreement). Until that finishes, health reports `503` and chats say so; files, git, settings and the shell already work. Then sign in to `kiro-cli` and start chatting.

## Capabilities

Vibekit is a full workspace in the browser. Everything below is reachable from any device viewing the same server.

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

- Switch modes/roles: bundled workflow modes (Default, Spec, Quick Spec, Bug Fix, Plan, Autonomous) and your own workspace agents from `.kiro/agents/`.
- Switch models on the fly, with per-model credit multipliers.
- Set reasoning effort (low through max).
- Approve or deny tool-call permission prompts; answer the agent's structured questions and MCP input (elicitation) forms.
- Plan handoff: edit a proposed plan in the editor, or run it directly.
- Rewind: branch a new conversation from any past turn, then merge it back or discard it.
- Subagents and agent-spawned terminals render as nested, collapsible cards.

**Editing and files** in the browser:

- File browser: navigate, rename, delete, download, and upload (dialog or drag-drop).
- Syntax-highlighted editor with edit/save and deep links to a line (`#L<n>`).
- Diff view against the last save or against git `HEAD`.
- Merge-conflict resolver: accept ours/theirs/both per hunk, with an AI "suggest" merge.
- Gutter markers on the lines the agent changed.

**A real terminal** in the browser:

- A PTY shell (via [web-terminal-engine](https://github.com/cplieger/web-terminal-engine)) that survives sleep and network drops, with a mobile key toolbar.

**Version control and forges** for GitHub, GitLab, Codeberg, and Gitea/Forgejo:

- Git panel: stage, commit (with an AI-written message), diff, and switch branches.
- Pull requests: list, create (with an AI-written description), merge, and close.
- Connect forge accounts via OAuth device flow or a token (driven by the `gh` / `glab` / `tea` CLIs).

**Safety nets** around the agent's file edits:

- Checkpoints: content-addressed per-file snapshots, independent of git; restore a turn's file edits or undo a single file, with cross-chat conflict detection and click-to-diff.
- Supervised mode: stage every agent file write for per-hunk accept/reject/merge before it touches disk; grant trust for the rest of a turn when you're confident.
- Permissions: a Cedar policy editor (allow/deny/ask per capability, with path scoping) and a "test a decision" explainer; one policy governs every tool call, shell commands included.
- Scope: checkpoints and supervised staging cover the agent's file-write channel. Changes made through shell commands or terminals (the agent's or yours) are approved via permissions but not snapshotted or staged; use git for those.

**MCP** server management:

- Add, edit, and remove MCP servers (local, or remote over HTTP/SSE) from the UI.
- OAuth (device flow, client id/secret), per-server auto-approve, live reconnect, and prompt/resource browsing.

**Workspace tools** and agent configuration:

- Manage installed tools: search a catalog of ~700 runtimes, language servers, and CLIs (compiled from the mise and aqua registries by [tool-catalog](https://github.com/cplieger/tool-catalog)), install them in the background with streamed progress, pin versions, or bring your own install command. The catalog refreshes itself on a schedule (`VIBEKIT_TOOL_CATALOG_REFRESH`, see the table below), keeping the last good copy on any failure, with a manual Refresh button in Settings → Tools. Every tool has an enable/disable switch: disabling uninstalls it but keeps the entry as a template, and fresh installs start with language-server templates (Go, TypeScript, Python, Rust) ready to switch on. Enabling a language server also activates kiro-cli's [code intelligence](https://kiro.dev/docs/cli/code-intelligence/) for the workspace automatically: LSP-backed navigation, rename, and diagnostics, live chats included, no restart. The detected-language set is frozen into `/workspace/.kiro/settings/lsp.json` at first activation; after adding a new language to the workspace, delete that file and vibekit re-initializes it on the next boot or language-server install.
- Knowledge bases: index workspace directories, with live progress.
- Spec board (`/specs`): a live requirements → design → tasks tree.
- Hooks: list, enable/disable, and run agent hooks; create one from chat context.
- Edit global custom instructions and browse steering docs, skills, and agents.

**Notifications** and PWA install:

- Installable as a PWA, with a home-screen icon and a share target.
- Web-push notifications when a turn finishes or the agent needs permission, even with the tab closed.

**Settings** and per-device preferences:

- Per-device layout (tabs, open files, shell) with light and dark themes.
- Account/subscription usage, a per-session context/credit meter, build versions, and a copyable diagnostics report.

## Configuration reference

The image ships working defaults; most setups only choose the volumes and how to expose the port.

- **Port:** `9847` (HTTP + SSE + the shell WebSocket).
- **Volumes:** `/config` persists chats, kiro-cli auth/state, installed tools, and settings; `/workspace` is your repositories.
- **User:** the compose above runs as `1000:1000` (see the first-boot ownership note). Run as root (`user: "0:0"`) to skip managing host ownership.
- **Health:** `GET /api/health` reports healthy once the server is up **and** the pinned `kiro-cli` is installed, runnable at that exact version, and has its auto-update switched off. Anything short of that answers `503` with the reason: `kiro-cli installing` during the first-boot download, `kiro-cli install retrying` between attempts, `kiro-cli unavailable` once they are exhausted, `kiro-cli required settings not enforced` when the pin could not be enforced. The web UI still starts in every one of those states (files, git, settings and the shell all work) and shows a banner; only chats wait. The install retries itself with backoff, so a failed download usually needs no action. To fix one by hand, repair `/config/tools/kiro-cli-versions` inside the container and run `curl -X POST localhost:9847/api/kiro-cli/rescan` (loopback only) to pick it up without a restart.

### Behind a reverse proxy (`WT_TRUSTED_PROXIES`)

The access log and the login/logout audit logs record a `client_ip`. How that address is resolved depends on how you expose vibekit:

- **Directly exposed** (no reverse proxy in front): leave `WT_TRUSTED_PROXIES` unset. `client_ip` is the address of the connecting socket, which cannot be forged at this layer. Any `X-Forwarded-For` header a client sends is ignored.
- **Behind a reverse proxy**: set `WT_TRUSTED_PROXIES` to your proxy's address range so `client_ip` shows the real client instead of the proxy's own address. It is a comma-separated list of CIDRs (bare IPs are accepted as single hosts), for example:

  ```yaml
  environment:
    WT_TRUSTED_PROXIES: "10.0.0.0/8,192.168.0.0/16"
  ```

`X-Forwarded-For` is honored **only** when the connecting peer falls inside `WT_TRUSTED_PROXIES`; otherwise the header is ignored and the socket peer is logged. List every proxy hop between the client and vibekit. This is spoof-safe by default: an empty, unset, or misconfigured value falls back to the unspoofable socket peer rather than trusting a client-supplied header.

### Host allowlist (`WT_ALLOWED_HOSTS`)

Set `WT_ALLOWED_HOSTS` to the exact hostnames/IPs you browse vibekit at (comma-separated, e.g. `WT_ALLOWED_HOSTS: "localhost,192.168.1.5,vibekit.example.com"`); a request with any other `Host` header is rejected with 403.

This blocks **DNS rebinding**: an attacker's page makes its own hostname resolve to your vibekit address, and because `Origin` and `Host` then agree, same-origin checks pass. The attack rides your own browser, so it reaches even a loopback- or LAN-bound deployment. An exact-`Host` allowlist breaks that chain. Requests made from the container itself (loopback socket peer with a loopback `Host`) are always admitted, so the image's healthcheck keeps working under any allowlist. Unset accepts every `Host` (and logs a startup warning); set it for any long-running deployment.

### Trusted install uids (`WT_TRUSTED_INSTALL_UIDS`)

Before installing kiro-cli, vibekit checks who can write each directory on the way to its install tree under `/config/tools`, and refuses to install when another identity can — what lands there is later executed by this container. Leave this **unset** (the default) and that check applies in full; that is the right setting for almost every deployment.

Set it only when the check refuses a volume you know is safe — typically a shared or network mount whose permissions grant an account you control:

```yaml
environment:
  WT_TRUSTED_INSTALL_UIDS: "3000"
```

Each uid you list is an assertion that the account is **already at least as privileged as this server**, so its write access gains it nothing. That is true of an administrator who already holds root on the host; it is false of an unprivileged account, and listing one of those hands it a way in instead of closing one. Entries that are not whole numbers above `0` are skipped with a warning naming the variable and how many were dropped, never their content. Sister app web-terminal-kiro reads the same variable for the same reason, which is why it carries no app-specific prefix.

### Extra browse roots (`VIBEKIT_BROWSE_ROOTS`)

The file browser sees exactly the granted roots (`/workspace` and `/config` by default) and nothing else in the container. Anything outside the grants (system directories, the image's own install tree, paths that don't exist yet) is denied by default rather than hidden by an enumerated block-list. To browse additional mounts, grant them explicitly with a colon-separated list of absolute paths:

```yaml
environment:
  VIBEKIT_BROWSE_ROOTS: "/tmp:/data"
```

Each grant must exist in the container (mount it via `volumes:` first); a malformed or missing entry is logged and skipped, never fatal. Credential and internal state files under `/config` (SSH keys, cloud tokens, chat store, MCP config) stay blocked regardless of grants.

### Extra kiro-cli launch flags (`VIBEKIT_KIRO_ACP_ARGS`)

An escape hatch for reaching a `kiro-cli acp` flag vibekit does not pass yet, without waiting for a release. Whitespace-separated, appended to every chat's launch command:

```yaml
environment:
  VIBEKIT_KIRO_ACP_ARGS: "-v"
```

Only the values are appended; nothing is interpreted as a shell command. Three flags are refused with a logged reason, because vibekit's own behaviour depends on them staying fixed:

- `--agent-engine` — vibekit speaks only the v3 wire, so selecting v1 or v2 would leave every chat unable to start.
- `--trust-all-tools` / `--trust-tools` — these have no effect on v3. Tool approval is the policy you edit in **Settings → Permissions**, so setting them here would look like turning approvals off without doing so.

vibekit already passes `--model` and `--effort` itself, and anything you set here is a starting value: changing the model or reasoning effort in the UI still takes precedence afterwards. Flags are logged by count only, never by value, so a mistyped value cannot leak into the logs. Not applied to the small background helper vibekit uses for chat titles.

### Environment variable reference

Every knob, including the ones detailed above. A malformed duration value logs a warning and falls back to its default.

| Variable | Description | Default |
| --- | --- | --- |
| `WT_TRUSTED_PROXIES` | Reverse-proxy CIDRs whose `X-Forwarded-For` resolves `client_ip`. See [Behind a reverse proxy](#behind-a-reverse-proxy-wt_trusted_proxies). | _(unset)_ |
| `WT_ALLOWED_HOSTS` | Exact hostnames/IPs vibekit answers for; anything else is rejected (anti-DNS-rebinding). See [Host allowlist](#host-allowlist-wt_allowed_hosts). | _(unset)_ |
| `WT_TRUSTED_INSTALL_UIDS` | Numeric uids whose write access to the kiro-cli install tree does not refuse the install. See [Trusted install uids](#trusted-install-uids-wt_trusted_install_uids). | _(unset)_ |
| `VIBEKIT_BROWSE_ROOTS` | Extra file-browser grants, colon-separated absolute paths. See [Extra browse roots](#extra-browse-roots-vibekit_browse_roots). | _(unset)_ |
| `VIBEKIT_KIRO_ACP_ARGS` | Extra `kiro-cli acp` launch flags for chats, whitespace-separated. See [Extra kiro-cli launch flags](#extra-kiro-cli-launch-flags-vibekit_kiro_acp_args). | _(unset)_ |
| `KIRO_WORK_DIR` | Directory chats and the shell start in. Must exist and be a directory; startup fails otherwise. | `/workspace` |
| `KIRO_CONFIG_DIR` | Persistent state root (chats, kiro-cli home, installed tools, settings). Must exist and be writable; startup fails otherwise. | `/config` |
| `KIRO_HOME` | Where vibekit resolves kiro-cli's per-user state tree (steering, settings, session files). | `$HOME/.kiro` |
| `VIBEKIT_TOOLS_DIR` | Tools engine install tree (`bin/`, `opt/`, `npm/`, `python/`) on the persistent volume. | `<KIRO_CONFIG_DIR>/tools` |
| `VIBEKIT_TOOL_CATALOG` | Image-baked tool catalog used at first boot and when offline, until a successfully fetched catalog replaces it. | `/opt/vibekit/tool-catalog.json` |
| `VIBEKIT_TOOL_CATALOG_URL` | Where catalog refreshes fetch from. Point it at a fork or mirror to decouple from the default publisher. | the [tool-catalog](https://github.com/cplieger/tool-catalog) latest-release artifact |
| `VIBEKIT_TOOL_CATALOG_REFRESH` | Catalog refresh cadence (Go duration, clamped to 1h-30d); `off` or `0` disables the schedule while the manual Refresh button and API stay available. | `24h` |
| `VIBEKIT_TOOL_CATALOG_OVERLAY` | Image-internal: display-patch overlay re-applied to every loaded catalog. An explicitly set path that does not resolve logs a warning and overlays are skipped. | `/opt/vibekit/catalog-overlays.json` |
| `VAPID_SUBJECT` | Contact URI embedded in the Web Push (VAPID) keys used for chat notifications. | `mailto:vibekit@noreply.invalid` |
| `VIBEKIT_AUTH_LOGIN_URL_TIMEOUT` | How long to wait for `kiro-cli login` to print the sign-in URL. | `10s` |
| `VIBEKIT_AUTH_LOGIN_PROCESS_CAP` | Hard cap on a whole login attempt, device-flow confirmation included. | `16m` |
| `VIBEKIT_AUTH_LOGOUT_TIMEOUT` | Timeout for `kiro-cli logout`. | `10s` |
| `VIBEKIT_AUTH_WHOAMI_TIMEOUT` | Timeout for the `kiro-cli whoami` sign-in status probe. | `5s` |

## Security

- **No built-in authentication**: put vibekit behind an authenticating reverse proxy and/or a private network (see the warning above).
- **No outbound telemetry.** vibekit reports nothing about you or your code to anyone. Every outbound request it makes is one you asked for: the AI provider `kiro-cli` is signed in to, any MCP server you configure yourself, the forge APIs (`gh` / `glab` / `tea`) when you use the git panel, and the public MCP registry when you search it from Settings → Tools. `kiro-cli`'s own telemetry is seeded **off** and is a toggle in Settings → General.
- Web push uses an SSRF-hardened transport.
- Debian base: a shell and the `kiro-cli` subprocess are required, so this is intentionally not distroless.
- Images are published with cosign signatures and SBOM attestations.

## kiro-cli

`kiro-cli` is downloaded and pinned on first boot rather than baked into the image (the AWS Customer Agreement governs redistribution, so you accept it by booting the container). Upgrades arrive by pulling a newer image tag; there is no in-place self-update.

The server owns that install. It fetches the pinned archive, checks its SHA-256 against the digest for your architecture, and unpacks it into its own directory under `/config/tools/kiro-cli-versions/<version>/`, published by a single rename once every file is on disk. An interrupted install leaves a directory with no completion marker, which the next boot deletes instead of trusting. Each boot also re-probes the binary it is about to use and requires it to report the pinned version, so a replaced or half-restored install is rejected rather than run.

One previous version is kept. If a new version turns out to be broken, the predecessor is still on the volume; anything older is pruned after the switch, along with the superseded agent-runtime trees that would otherwise grow by ~240 MB per version.

`docker exec <container> kiro-cli --version` keeps working: `/config/tools/bin/kiro-cli` is maintained as a symlink to the active version. It is a convenience for you, not part of the install; vibekit itself always runs the absolute versioned path.

## Related projects

- [web-terminal-kiro](https://github.com/cplieger/web-terminal-kiro): the sister app, a raw browser terminal that drives kiro-cli's own TUI instead of this chat-first UI.
- [web-terminal-engine](https://github.com/cplieger/web-terminal-engine): the terminal engine (Go PTY/VT + TypeScript renderer) behind vibekit's shell.
- [pinstall](https://github.com/cplieger/pinstall): the digest-pinned install library that downloads, verifies and activates the `kiro-cli` release described above.
- [actions](https://github.com/cplieger/actions): the client-side action framework vibekit's UI is built on.

## Contributing

Architecture, the invariants you must not break, and local build/test instructions are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE).
