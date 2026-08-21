# Security assurance case: vibekit

This extends the shared
[default assurance case](https://github.com/cplieger/.github/blob/main/assurance-case.md)
with the threat model specific to `vibekit`. Read that first. vibekit is
**alpha**; this case is honest about that and the [roadmap](../ROADMAP.md)
lists the hardening in progress.

## What this is

A self-hosted web front-end that drives a kiro-cli coding agent over ACP, with
chat persistence, subagents, MCP integration, and push notifications. By
design it can run an agent that executes commands and edits files, so its
**intended capability is powerful**; the security model is about who can reach
it and keeping per-chat data isolated, not about sandboxing the agent.

## Security model

vibekit is a **trusted-operator tool behind a network/auth boundary**, not a
public multi-tenant service. In a self-hosted deployment it is reachable only on the internal
network (LAN-gated, behind the reverse proxy). The agent's ability to run
commands is the product, not a vulnerability; the boundary is "only the operator
can reach vibekit."

## Threats and mitigations

| Threat                                                    | Mitigation                                                                                         | Evidence                                   |
| --------------------------------------------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| Cross-chat data access (one chat reading another's blobs) | chat-scoped blob access: `ReadBlob` returns 404 if the chat's event log doesn't reference the SHA  | checkpoint/blob tests                      |
| MCP secret mishandling                                    | structured secret round-trip in the MCP layer; secrets not logged                                  | `internal/mcp`, mcp tests                  |
| Push-notification crypto errors                           | push payload crypto exercised under fuzz                                                           | `internal/push/crypto_fuzz_test.go`        |
| Malformed ACP / wire input                                | hardened decoders; large Go + property/fuzz suite (350+ test files, 160+ fuzz targets)             | weekly fuzz + gremlins                     |
| Stale/empty embedded UI shipped                           | CI image smoke test starts the container and asserts the health endpoint serves                    | image smoke test (CI docker job)           |
| Reaching vibekit without authorisation                    | network/auth boundary (LAN gate + reverse proxy)                                                   | self-hosted deployment                     |

## Residual risks (stated plainly)

- **Alpha.** The surface is still being tested and hardened; do not expose
  vibekit to untrusted networks.
- The agent can execute commands and modify files by design; anyone who can
  reach an authenticated session has that capability. Network/auth isolation is
  the control, and it is a deployment responsibility.

Report vulnerabilities privately per
[SECURITY.md](https://github.com/cplieger/.github/blob/main/SECURITY.md).
