# vibekit

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/size.json)](https://github.com/cplieger/vibekit/pkgs/container/vibekit)
![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)
![base: Debian](https://img.shields.io/badge/base-Debian-A81D33?logo=debian)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/coverage.json)](https://github.com/cplieger/vibekit/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/vibekit/badges/mutation.json)](https://github.com/cplieger/vibekit/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13224/badge)](https://www.bestpractices.dev/projects/13224)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/vibekit/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/vibekit)
[![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/vibekit/releases)

A browser-based front-end for the Kiro CLI — chat with an AI coding agent from any device, with shared sessions, a live terminal, an editor, and git/forge workflows.

## What it is

Vibekit is a Go web server that drives `kiro-cli` over the Agent Client Protocol (ACP). Each active chat runs one `kiro-cli acp` subprocess (the "bridge"); multiple devices and tabs on the same chat share it. The server is the single source of truth and the client is a pure projection of server state — every mutation goes through `POST /api/command` and is echoed back over SSE, so there is no optimistic local rendering and no multi-device drift.

## Features

- **Multi-device chat** — open the same conversation on phone and desktop; both stay in sync via SSE fan-out. One JSON file per chat, atomic writes, full history.
- **Streaming with reasoning** — token-by-token markdown rendering with a collapsible "thinking" block, batched for smoothness.
- **Live terminal** — a real PTY shell in the browser (powered by [`@cplieger/web-terminal-engine`](https://github.com/cplieger/web-terminal-engine)) plus per-agent command terminals.
- **Editor + diff + conflict views** — read/edit workspace files, view tool-call diffs, resolve merge conflicts inline.
- **Checkpoints** — a content-addressed per-file snapshot system with two-phase atomic restore and cross-chat conflict detection (independent of git).
- **Supervised mode** — a per-chat write-gate that stages every agent file write for per-hunk accept/reject/merge before it touches disk.
- **Subagent crew monitor**, **rewind** (branch a chat from a past turn), **MCP integration** (add/configure MCP servers from the UI), and **git + forge workflows** (GitHub/GitLab/Gitea via the first-party `gh`/`glab`/`tea` CLIs — PRs, issues, releases).
- **PWA** with web-push notifications for turn-complete and permission prompts.

## Run it

```yaml
# compose.yaml
services:
  vibekit:
    image: ghcr.io/cplieger/vibekit:latest
    user: "1000:1000"  # match your host user
    ports:
      - "9847:9847"
    volumes:
      - ./config:/config        # chats, kiro-cli auth/state, tools
      - ./workspace:/workspace  # your repos
    restart: unless-stopped
```

Before the first start, create and own the bind-mount directories, since the container runs as `user: "1000:1000"`. The entrypoint does not `chown` them, so a root-owned host directory makes first boot fail with `failed to create config directories`:

```bash
mkdir -p /opt/appdata/vibekit/config /opt/appdata/vibekit/workspace
chown -R 1000:1000 /opt/appdata/vibekit
```

To skip managing host ownership, run as root instead with `user: "0:0"` (less secure).

`kiro-cli` is downloaded and pinned on first boot (it is not redistributed in the image, per the AWS Customer Agreement). On first launch, authenticate `kiro-cli` and start chatting.

## Security

Network-exposed: put it behind an authenticating reverse proxy — vibekit has its own password/OIDC auth, but it controls an agent with shell and filesystem access to `/workspace`. Web-push uses an SSRF-hardened transport. Debian base (a shell + the `kiro-cli` subprocess are required, so this is not distroless). Images are published with cosign signatures and SBOM attestations.

### Client IP logging (`TRUSTED_PROXIES`)

The access log and the login/logout audit logs record a `client_ip`. How that address is resolved depends on how you expose vibekit:

- **Directly exposed** (no reverse proxy in front): leave `TRUSTED_PROXIES` unset. `client_ip` is the address of the connecting socket, which cannot be forged at this layer. Any `X-Forwarded-For` header a client sends is ignored.
- **Behind a reverse proxy**: set `TRUSTED_PROXIES` to your proxy's address range so `client_ip` shows the real client instead of the proxy's own address. It is a comma-separated list of CIDRs (bare IPs are accepted as single hosts), for example:

  ```yaml
  environment:
    TRUSTED_PROXIES: "10.0.0.0/8,192.168.0.0/16"
  ```

`X-Forwarded-For` is honored **only** when the connecting peer falls inside `TRUSTED_PROXIES`; otherwise the header is ignored and the socket peer is logged. List every proxy hop between the client and vibekit. This is spoof-safe by default: an empty, unset, or misconfigured value falls back to the unspoofable socket peer rather than trusting a client-supplied header.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0. See [LICENSE](LICENSE).
