# vibekit

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/size.json)](https://github.com/cplieger/vibekit/pkgs/container/vibekit)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Debian](https://img.shields.io/badge/base-Debian-A81D33?logo=debian)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/coverage.json)](https://github.com/cplieger/vibekit/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/mutation.json)](https://github.com/cplieger/vibekit/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13224/badge)](https://www.bestpractices.dev/projects/13224)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/vibekit/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/vibekit)
[![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/vibekit/releases)

A browser-based front-end for the **Kiro CLI**: chat with an AI coding agent from any device, with a live terminal, a file editor, and git/forge workflows in the same tab.

Vibekit runs `kiro-cli` as an Agent Client Protocol (ACP) subprocess and wraps it in a full workspace UI. The **server is the single source of truth**: every action is persisted and echoed to every connected client over Server-Sent Events, so a conversation open on your phone and your desktop stays in sync.

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
    container_name: vibekit
    user: "${PUID:-1000}:${PGID:-1000}"  # from .env; must own the bind mounts
    ports:
      - "9847:9847"
    volumes:
      - "./config:/config"  # chats, kiro-cli auth/state, tools
      - "./workspace:/workspace"  # your repos
    restart: unless-stopped
```

Before the first start, create the bind-mount directories and give them to that UID (1000 unless you set `PUID`/`PGID`). The entrypoint does not `chown` them, so a root-owned host directory makes first boot fail with `failed to create required directories`:

```bash
mkdir -p ./config ./workspace
chown -R "${PUID:-1000}:${PGID:-1000}" ./config ./workspace
```

To skip managing host ownership, run as root instead with `user: "0:0"` (less secure).

Open <http://localhost:9847>. The UI comes up right away, and `kiro-cli` is downloaded and verified in the background on first boot. Until that finishes, health reports `503` and chats say so, while files, git, settings and the shell already work. Then sign in to `kiro-cli` and start chatting.

## Capabilities

Vibekit is a full workspace in the browser, and everything below is reachable from any device viewing the same server.

**Chat:** conversations kept in sync across devices over SSE, with full history in one file per chat, streaming markdown responses, and collapsible reasoning ("thinking") blocks. Send mid-turn and your message joins the running turn. Prefix a message with `!` to run a shell command. `/compact` compacts the context now, `/drop` ends a wedged turn, and `/goal` sets an objective the agent works toward across turns until it meets the goal or its iteration budget runs out. Attach files by drag-drop, paste, or the composer's `+` menu; PDF, CSV and Office documents reach the agent as documents, images as images, and anything else as a path it opens with its file tools. Also find-in-chat, per-chat export, a cross-chat search, a History view over past conversations and workflow runs, and configurable chat retention.

**Agent control:** switch modes (Default, Spec, Quick Spec, Bug Fix, Plan, Autonomous, plus your own workspace agents from `.kiro/agents/`), switch models mid-conversation, set reasoning effort from low to max, answer permission prompts, structured questions and MCP elicitation forms, and fork a tangent that leaves the original chat untouched. Subagent work renders as collapsible cards, and the agent's own terminals get tabs in the shell panel.

**Editing, files and terminal:** a file browser, a recursive content search with include and exclude patterns, a syntax-highlighted editor with deep links to a line (`#L<n>`), diffs against the last save or against git `HEAD`, a merge-conflict resolver that takes ours, theirs or both per hunk, and a PTY shell (via [web-terminal-engine](https://github.com/cplieger/web-terminal-engine)) that survives sleep and network drops.

**Version control and forges** for GitHub, GitLab, Codeberg, and Gitea/Forgejo: stage, commit, diff and switch branches; list, create, merge and close pull requests, with AI-written commit messages and descriptions; connect accounts by OAuth device flow or a token, driven by the `gh` / `glab` / `tea` CLIs.

**Safety nets** around the agent's file edits:

- Rewind: send the conversation back to an earlier message of yours. The agent's file edits roll back with the transcript, from snapshots the agent runtime takes independently of git.
- Supervised mode: hold the whole turn for review instead of approving each write. One approval lists every file the turn touched, renames and deletes included, and you keep or discard each one.
- Permissions: a Cedar policy editor (allow/deny/ask per capability, with path scoping) and a "test a decision" explainer. One policy governs every tool call, shell commands included.
- Scope: rewind and supervised review cover the agent's file-write channel. Changes made through shell commands or terminals go through the policy but are not snapshotted, so use git for those. A held write does land on disk while you review it, so a watcher or dev server sees it before you decide.

**MCP:** add, edit and remove servers (local, or remote over HTTP/SSE), with per-server auto-approve, live reconnect, and prompt and resource browsing. For a server that needs OAuth, vibekit registers itself automatically or takes a client id and secret you already have, then shows a sign-in link.

**Workspace tools:** install runtimes, language servers and CLIs from a catalog of ~700 (compiled from the mise and aqua registries by [tool-catalog](https://github.com/cplieger/tool-catalog)), in the background, pinned to a version or with an install command of your own. When you enable a language server, vibekit also activates kiro-cli [code intelligence](https://kiro.dev/docs/cli/code-intelligence/) for the workspace: LSP-backed navigation, rename and diagnostics, live chats included, no restart. It freezes the detected-language set into `/workspace/.kiro/settings/lsp.json` at first activation, so after you add a language to the workspace, delete that file; vibekit re-initializes it on the next boot.

**Workspace configuration** on the `/docs` page: the whole `.kiro` inventory with its front-matter (steering docs, skills, agents, specs, hooks), knowledge bases you index, hooks you enable, and workflow runs you launch, pause, resume, cancel or schedule. Settings holds global custom instructions, per-device layout with light and dark themes, account usage, a context and credit meter, and a copyable diagnostics report.

**Notifications:** installable as a PWA, with web-push notifications when a turn finishes, a pull request's checks settle, or the agent needs permission, even with the tab closed. The turn and pull-request kinds each have their own switch; the permission notice has none, because nothing else tells you off-screen that a turn waits on you.

## Configuration reference

The image ships working defaults; most setups only choose the volumes and how to expose the port.

- **Port:** `9847` (HTTP + SSE + the shell WebSocket).
- **Volumes:** `/config` persists chats, kiro-cli auth and state, installed tools, and settings; `/workspace` is your repositories.
- **User:** the compose above runs as `1000:1000`; see the first-boot ownership note.
- **Health:** `GET /api/health` reports healthy once the server is up **and** the pinned `kiro-cli` is installed, runnable at that exact version, and has its auto-update switched off. Anything short of that answers `503` with a reason: `kiro-cli installing`, `kiro-cli install retrying`, `kiro-cli unavailable` once the attempts are exhausted, or `kiro-cli required settings not enforced`. The UI still starts in every one of those states and shows a banner; only chats wait, and the install retries itself with backoff. To repair one by hand, fix `/config/tools/kiro-cli-versions` inside the container and `curl -X POST localhost:9847/api/kiro-cli/rescan` (loopback only) to pick it up without a restart.

### Behind a reverse proxy (`TRUSTED_PROXIES`)

The access log and the login/logout audit logs record a `client_ip`. Leave `TRUSTED_PROXIES` unset when vibekit is directly exposed: `client_ip` is then the connecting socket's address, which a client cannot forge, and any `X-Forwarded-For` header it sends is ignored. Behind a reverse proxy, set it to the address range of every hop, comma-separated CIDRs (a bare IP counts as a single host), so `client_ip` shows the real client:

```yaml
environment:
  TRUSTED_PROXIES: "10.0.0.0/8,192.168.0.0/16"
```

`X-Forwarded-For` is honored **only** when the connecting peer falls inside the list. An empty, unset, or malformed value falls back to the socket peer, so the default is spoof-safe.

### Host allowlist (`ALLOWED_HOSTS`)

Set `ALLOWED_HOSTS` to the exact hostnames and IPs you browse vibekit at (comma-separated, for example `ALLOWED_HOSTS: "localhost,192.168.1.5,vibekit.example.com"`); a request with any other `Host` header is rejected with 403.

Set it for any long-running deployment, because it is what blocks **DNS rebinding**: an attacker's page makes its own hostname resolve to your vibekit address, `Origin` and `Host` then agree, and the same-origin check passes. That attack rides your own browser, so it reaches even a loopback- or LAN-bound deployment. Requests from the container itself are always admitted, so the image's healthcheck keeps working. Unset accepts every `Host` and warns at startup.

### Trusted install uids (`TRUSTED_INSTALL_UIDS`)

Before it installs kiro-cli, vibekit checks who can write each directory on the way to its install tree under `/config/tools`, and refuses the install when another identity can, because this container later executes what lands there. Leave this **unset** (the default) for almost every deployment, and set it only when the check refuses a volume you know is safe, typically a shared or network mount whose permissions grant an account you control:

```yaml
environment:
  TRUSTED_INSTALL_UIDS: "3000"
```

Each uid you list is an assertion that the account is **already at least as privileged as this server**, so its write access gains it nothing. That is true of an administrator who already holds root on the host; it is false of an unprivileged account, and listing one of those hands it a way in instead of closing one. An entry that is not a whole number above `0` is skipped with a warning that names the variable and the count, never the content.

### Extra browse roots (`VIBEKIT_BROWSE_ROOTS`)

The file browser sees the granted roots (`/workspace` and `/config` by default) and nothing else in the container. To browse another mount, grant it with a colon-separated list of absolute paths:

```yaml
environment:
  VIBEKIT_BROWSE_ROOTS: "/tmp:/data"
```

Mount each grant with `volumes:` first; a missing or malformed entry is skipped with a warning, never fatal. Credential and internal state files under `/config` (SSH keys, cloud tokens, chat store, MCP config) stay blocked whatever you grant.

### Extra kiro-cli launch flags (`VIBEKIT_KIRO_ACP_ARGS`)

An escape hatch for a `kiro-cli acp` flag vibekit does not pass yet, without waiting for a release. Whitespace-separated, appended to every chat's launch command:

```yaml
environment:
  VIBEKIT_KIRO_ACP_ARGS: "-v"
```

Only the values are appended; nothing is interpreted as a shell command. Five flags are refused with a logged reason, because each one breaks a chat or does nothing: `--agent-engine` (vibekit speaks only the v3 wire), `--trust-all-tools` and `--trust-tools` (inert on v3, where tool approval is the policy you edit in **Settings → Permissions**), and `--model` and `--effort` (kiro-cli rejects both on the v3 wire and exits before the session opens; pick them per chat in the composer instead).

Anything else you set is a starting value the UI still overrides. Flags are logged by count only, never by value, so a mistyped value cannot leak into the logs.

### Agent-launched workflow runs (`VIBEKIT_AGENT_WORKFLOWS`)

The chat agent can start a workflow run itself: it holds the workflow tools (`run_workflow`, `inspect_workflow`, `update_workflow`, `validate_workflow`, `send_message`), so a request like "run the publish workflow" starts the run instead of describing it. Runs you launch yourself from **Workflows** on `/docs` are unaffected.

An agent-launched run has two cosmetic rough edges: after a reload, a progress note the run posts back renders as a user bubble, and pause, resume and retry work only on a run you started from the Workflows tab, though Stop always works.

To switch the capability off, set the variable to `false` (also `0`, `no`, or `off`):

```yaml
environment:
  VIBEKIT_AGENT_WORKFLOWS: "false"
```

The agent then loses the workflow tools and answers about workflows in prose; everything you launch yourself keeps working. A value that cannot be read as a boolean warns and leaves the capability **on**, so a typo cannot disable it by accident. The change takes effect on the next chat, so restart the container to apply it everywhere.

### Agent environment variables (`VIBEKIT_ALLOW_AGENT_ENV`)

When the agent runs a command, it can also ask for environment variables to be set for it. Most are ordinary (`CGO_ENABLED`, `GOFLAGS`, `TERM`), but a few carry no data and instead change what a program _executes_: `LD_PRELOAD`, `GIT_SSH_COMMAND` and `BASH_ENV` each redirect execution. vibekit refuses those, because approving a command must approve **that** command, and the agent's variables take precedence over vibekit's own. The refused list is kiro-cli's own, a harmless value is still accepted (`GIT_PAGER=cat` and `PAGER=` keep working), and the refusal names the variable so the agent can retry without it.

If you genuinely need one (a profiler that preloads a library, a vendored `NODE_PATH`), name it:

```yaml
environment:
  VIBEKIT_ALLOW_AGENT_ENV: "LD_PRELOAD,NODE_PATH"
```

Comma-separated, granting only the names you list. It applies to what the **agent** asks for, not to variables you set on the container yourself.

### Credentials in the container environment (`VIBEKIT_ALLOW_BRIDGE_ENV`)

The other direction: kiro-cli and everything it runs inherit whatever you put in the container's `environment:`, so a `GITHUB_TOKEN` you added for some unrelated reason is a credential every agent turn can read and use.

vibekit drops credential-shaped names on the way down and logs which ones, by name only: a name ending in `_TOKEN` or `_SECRET`, plus `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Ordinary variables are untouched, `AWS_REGION` and `AWS_PROFILE` included. Keep credentials out of the container environment anyway: forge tokens belong in `gh` / `glab` / `tea`'s own stores, which is where the git panel puts them. If a variable's name merely reads like a credential, name it:

```yaml
environment:
  VIBEKIT_ALLOW_BRIDGE_ENV: "BUILDKITE_AGENT_TOKEN"
```

### OS packages

The tools engine installs OS packages now, so there is no separate variable for them. Add one from **Settings → Tools** the way you add anything else, or by name with an explicit source:

```json
{ "tools": { "gcc": { "source": "apt:gcc" }, "libc6-dev": { "source": "apt:libc6-dev" } } }
```

Two cases need this. Go work that runs `go test -race` needs a C compiler the image does not ship. And a runtime the engine installs can link a shared library the image lacks, so the tool installs and then refuses to start; the tools panel names the missing library on that runtime's row.

What an `apt:` entry buys over `apt-get install` in the shell is the record: the entry is on the `/config` volume, so a container recreate reinstalls the package instead of losing it, and the row reports the installed version. Removing the entry is a logged no-op rather than an uninstall — apt packages are shared, and the engine will not remove one it cannot prove nothing else needs.

Plain package names only. A version pin (`pkg=1.2`), `pkg:arch`, `pkg/release`, a trailing `-` (apt reads that as a removal), a name absent from the package index, and a pure virtual package such as `awk` (name a concrete provider such as `mawk`) are each refused with the reason. Pinning an entry holds the installed version and marks it held in dpkg, so apt will not move it as a dependency of something else either.

The `APT_PACKAGES` variable this replaced is gone and is not read at all. Remove it from your compose file and add each package as a manifest entry instead — a clean break rather than a migration, so nothing is imported for you.

### Environment variable reference

Every knob, the ones detailed above included. A malformed duration warns and falls back to its default.

| Variable | Description | Default |
| --- | --- | --- |
| `TRUSTED_PROXIES` | Reverse-proxy CIDRs whose `X-Forwarded-For` resolves `client_ip`. See [Behind a reverse proxy](#behind-a-reverse-proxy-trusted_proxies). | _(unset)_ |
| `ALLOWED_HOSTS` | Exact hostnames/IPs vibekit answers for; anything else is rejected (anti-DNS-rebinding). See [Host allowlist](#host-allowlist-allowed_hosts). | _(unset)_ |
| `TRUSTED_INSTALL_UIDS` | Numeric uids whose write access to the kiro-cli install tree does not refuse the install. See [Trusted install uids](#trusted-install-uids-trusted_install_uids). | _(unset)_ |
| `VIBEKIT_BROWSE_ROOTS` | Extra file-browser grants, colon-separated absolute paths. See [Extra browse roots](#extra-browse-roots-vibekit_browse_roots). | _(unset)_ |
| `VIBEKIT_KIRO_ACP_ARGS` | Extra `kiro-cli acp` launch flags for chats, whitespace-separated. See [Extra kiro-cli launch flags](#extra-kiro-cli-launch-flags-vibekit_kiro_acp_args). | _(unset)_ |
| `VIBEKIT_AGENT_WORKFLOWS` | Whether the chat agent can start workflow runs itself. See [Agent-launched workflow runs](#agent-launched-workflow-runs-vibekit_agent_workflows). | `true` |
| `VIBEKIT_ALLOW_AGENT_ENV` | Execution-redirecting variable names the agent can set for its own commands, comma-separated. See [Agent environment variables](#agent-environment-variables-vibekit_allow_agent_env). | _(unset)_ |
| `VIBEKIT_ALLOW_BRIDGE_ENV` | Credential-shaped names to inherit into kiro-cli anyway, comma-separated. See [Credentials in the container environment](#credentials-in-the-container-environment-vibekit_allow_bridge_env). | _(unset)_ |
| `KIRO_WORK_DIR` | Directory chats and the shell start in. Must exist and be a directory; startup fails otherwise. | `/workspace` |
| `KIRO_CONFIG_DIR` | Persistent state root (chats, kiro-cli home, installed tools, settings). Must exist and be writable; startup fails otherwise. | `/config` |
| `KIRO_HOME` | Where vibekit resolves kiro-cli's per-user state tree (steering, settings, session files). | `$HOME/.kiro` |
| `VIBEKIT_TOOLS_DIR` | Tools engine install tree (`bin/`, `opt/`, `npm/`, `python/`) on the persistent volume. | `<KIRO_CONFIG_DIR>/tools` |
| `VIBEKIT_TOOL_CATALOG` | Image-baked tool catalog used at first boot and when offline, until a fetched catalog replaces it. | `/opt/vibekit/tool-catalog.json` |
| `VIBEKIT_TOOL_CATALOG_URL` | Where catalog refreshes fetch from; point it at a fork or mirror to leave the default publisher. | the [tool-catalog](https://github.com/cplieger/tool-catalog) latest-release artifact |
| `VIBEKIT_TOOL_CATALOG_REFRESH` | Catalog refresh cadence (Go duration, clamped to 1h-30d); `off` or `0` disables the schedule and keeps the manual refresh. | `24h` |
| `VIBEKIT_BUNDLED_TOOLS` | Image-internal file naming the tools vibekit bundles — the ones it needs and the ones it recommends — merged over every loaded catalog. A path that does not resolve warns and is skipped, which leaves the seeded language servers unresolvable. | `/opt/vibekit/bundled-tools.json` |
| `VAPID_SUBJECT` | Contact URI embedded in the Web Push (VAPID) keys used for chat notifications. | `mailto:vibekit@noreply.invalid` |
| `VIBEKIT_AUTH_LOGIN_URL_TIMEOUT` | How long to wait for `kiro-cli login` to print the sign-in URL. | `10s` |
| `VIBEKIT_AUTH_LOGIN_TIMEOUT` | Wall-clock timeout for a whole login attempt, device-flow confirmation included. | `16m` |
| `VIBEKIT_AUTH_LOGOUT_TIMEOUT` | Timeout for `kiro-cli logout`. | `10s` |
| `VIBEKIT_AUTH_WHOAMI_TIMEOUT` | Timeout for the `kiro-cli whoami` sign-in status probe. | `5s` |

## Security

- **No built-in authentication**: see the warning above.
- **No outbound telemetry.** Every outbound request vibekit makes is one you asked for: the AI provider `kiro-cli` is signed in to, any MCP server you configure, the forge APIs (`gh` / `glab` / `tea`) when you use the git panel, and the public MCP registry when you search it. `kiro-cli`'s own telemetry is seeded **off** and is a toggle in Settings → General.
- Web push uses an SSRF-hardened transport.
- Debian base: a shell and the `kiro-cli` subprocess are required, so this is intentionally not distroless.
- Images are published with cosign signatures and SBOM attestations.

## kiro-cli

`kiro-cli` is downloaded and pinned on first boot rather than baked into the image (the AWS Customer Agreement governs redistribution, so you accept it by booting the container). Upgrades arrive by pulling a newer image tag; there is no in-place self-update.

The server owns that install. It verifies the pinned archive's SHA-256 against the digest for your architecture, installs it under `/config/tools/kiro-cli-versions/<version>/`, and re-probes it on every boot, so a replaced or half-restored install is rejected rather than run. The previous version stays on the volume as the fallback for a broken new one. `docker exec <container> kiro-cli --version` keeps working, because `/config/tools/bin/kiro-cli` is a symlink to the active version; vibekit itself always runs the absolute versioned path.

## Related projects

- [web-terminal-kiro](https://github.com/cplieger/web-terminal-kiro): the sister app, a raw browser terminal that drives kiro-cli's own TUI instead of this chat-first UI.
- [web-terminal-engine](https://github.com/cplieger/web-terminal-engine): the terminal engine (Go PTY/VT + TypeScript renderer) behind vibekit's shell.
- [pinstall](https://github.com/cplieger/pinstall): the digest-pinned install library behind the `kiro-cli` install above.
- [actions](https://github.com/cplieger/actions): the client-side action framework vibekit's UI is built on.

## Contributing

Architecture, the invariants you must not break, and local build/test instructions are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE).
