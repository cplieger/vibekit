# Extra kiro-cli launch flags (`VIBEKIT_KIRO_ACP_ARGS`)

An escape hatch for a `kiro-cli acp` flag vibekit does not pass yet, without
waiting for a release. Whitespace-separated, appended to every chat's launch
command:

```yaml
environment:
  VIBEKIT_KIRO_ACP_ARGS: "-v"
```

Only the values are appended; nothing is interpreted as a shell command.

Five flags are refused with a logged reason, because each one breaks a chat or
does nothing:

| Flag                                 | Why it is refused                                                                            |
| ------------------------------------ | -------------------------------------------------------------------------------------------- |
| `--agent-engine`                     | vibekit speaks only the v3 wire                                                              |
| `--trust-all-tools`, `--trust-tools` | inert on v3, where tool approval is the policy you edit in **Settings → Permissions**        |
| `--model`, `--effort`                | kiro-cli rejects both and exits before the session opens; pick them per chat in the composer |

Anything else you set is a starting value the UI still overrides. Flags are
logged by count only, never by value, so a mistyped value cannot leak into the
logs.
