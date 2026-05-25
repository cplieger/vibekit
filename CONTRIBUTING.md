# Contributing to vibekit

Notes on conventions and architecture. Most of the codebase is
discoverable, but a few patterns are load-bearing and easy to miss
when adding new features.

## User-initiated mutations go through the actions framework

Any button click, form submit, or keystroke that triggers an
HTTP request or a `transport.send` must go through the action
framework at `web/static-src/actions/`.

```ts
// actions/files.ts
export const deleteFile = apiAction<string, void>({
  name: "files.delete",
  request: (path) => ({ method: "POST", path: "/api/files/action", body: { action: "delete", path } }),
  optimistic: (path) => store.markPending(path),
  rollback:   (path) => store.unmarkPending(path),
  error: "Couldn't delete file",
});

// callsite
const ok = await deleteFile.dispatch(somePath);
if (ok === null) return;  // toast already fired
```

The framework handles:
- Optimistic UI + automatic rollback on failure.
- Toast on success / error with sensible defaults.
- Cancellation via `AbortController`.
- Action log in the registry for devtools / loading-state queries.

See `web/static-src/actions/README.md` for the full guide.

A regression test (`actions/lint.test.ts`) catches new
`void apiPost(...)` / `void transport.send(...)` calls outside the
allowlist of background paths. If you're adding a legitimate
background poll, add the file to `BACKGROUND_ALLOWLIST` with a
one-line comment. Otherwise, write an action.

## Toasts vs banners vs inline errors

| Surface          | Use for                                                             |
|------------------|---------------------------------------------------------------------|
| Toast (transient) | User-initiated mutation failures. Errors stick until dismissed.    |
| Banner (persistent) | Critical state the user must see across navigation (rate limits, model unavailable, MCP unhealthy). Lives in `banner-stack.ts`. |
| Inline form error | Form validation. Surface near the field that failed. |
| Log only         | Background polls, SSE bus errors, infrastructure exceptions, fire-and-forget cleanup. |

Don't double-fire — if a banner is already up, the action's `error`
field can be `false` to suppress the toast.

## Running checks locally

```sh
cd web/static-src
/config/tools/bin/tsgo -config tsconfig.json     # typecheck
node_modules/.bin/vitest run --no-coverage       # frontend tests
go test ./...                                    # backend tests (run from /workspace/vibekit/web)
```

## Branch policy

Feature work goes on a dedicated branch named after the change
(e.g. `style/git-empty-states-and-pr-header`, `feat/actions`).
Direct push to `main` is reserved; everything else opens a PR.
