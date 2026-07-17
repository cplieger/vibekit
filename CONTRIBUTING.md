# Contributing to vibekit

vibekit is a browser front-end for the Kiro CLI. Each active chat runs one
`kiro-cli acp` subprocess (the "bridge"); the Go server owns all state and the
TypeScript client is a pure projection of it. Most of the codebase is
discoverable by reading it, but a handful of patterns are load-bearing and easy
to break by accident. This guide covers the architecture at a high level, the
invariants you must not violate, and how to build, run, and check your work
locally.

## Project overview

- The **server** (Go, at the repo root) is the single source of truth. Every
  mutation goes through `POST /api/command`, is persisted, and is echoed back to
  clients over Server-Sent Events (`GET /api/events`).
- The **client** (TypeScript in `static-src/`, compiled and embedded into the
  binary) renders server state. It never displays anything the server has not
  first persisted and broadcast.
- One `kiro-cli acp` bridge backs each active chat; multiple tabs or devices on
  the same chat share that bridge.

If you only read one thing before changing behaviour, read the
[Invariants](#invariants-do-not-break) section.

## Architecture

This is a map of package and module boundaries, not a file manifest. Discover
the real tree with `go list ./...` or by browsing `internal/` and `static-src/`.

### Server (repo root)

- `main.go` / `embed.go` — composition root: load config, construct the store,
  hub, bridge, and push service, register HTTP routes, embed the compiled
  static assets. Wiring only.
- `internal/api/` — interface contracts (`ChatStore`, `Hub`, `ACPBridge`,
  `PushService`) plus the domain types (chat, message, tool call, plan, usage).
  Every other package depends only on this one. Includes `httputil.go` response
  helpers — use them, never hand-craft JSON.
- `internal/chat/` — persistence: one JSON file per chat, written atomically.
  `Mutate` is the only write path.
- `internal/hub/` — command dispatch, SSE broadcast, ACP-to-domain event
  translation, bridge buffer aggregation, and the global PTY shell.
- `internal/bridge/` — `kiro-cli acp` subprocess lifecycle, capability
  handshake, and the filesystem read/write handlers.
- `internal/translate/` — ACP notification handlers that turn raw `kiro-cli`
  events into vibekit domain events.
- `internal/command/` — handlers for each `POST /api/command` type.
- `internal/checkpoint/` — content-addressed file-snapshot store with a
  two-phase atomic restore.
- `internal/permissions/`, `internal/forges/`, `internal/git/`,
  `internal/mcp/`, `internal/push/`, `internal/auth/`,
  `internal/server/` — feature subsystems (tool-approval policy, forge CLI
  orchestration, git handlers, MCP, web push, kiro-cli identity endpoints, HTTP
  middleware and routing).
- The tools engine is the external
  [`cplieger/toolbelt`](https://github.com/cplieger/toolbelt) library, wired in
  `internal/composition` (manifest + catalog + reconciler; job events reach the
  SSE hub through its `Config` callbacks). `/api/tools` is the library's
  `httpapi` projection mounted under vibekit's middleware; only
  `/api/tools/status` (feature-gating PATH probes) is app code.
- `internal/buffer/`, `internal/settings/`, `internal/steering/`,
  `internal/workspace/`, `internal/kiroauth/`, `internal/version/`, and the
  other small packages — focused helpers.

Server dependencies flow one direction: composition root → hub → API contracts
→ feature packages. No reverse imports.

### Client (`static-src/`, compiled to `static/`)

- `app.ts` — the composition root. It is the only module that imports and wires
  everything; nothing imports from it.
- `transport.ts`, `sw.ts`, `upload.ts`, `api-client.ts` — the only modules
  allowed to call `fetch()` directly. Everything else goes through
  `api-client.ts` (`apiGet` / `apiPost` / `apiDelete` and typed variants).
- `store.ts` (+ `store-load.ts`, `store-signals.ts`) — the chat-state singleton,
  a projection of server state.
- `bus.ts` — cross-module event bus that breaks import cycles.
- `dom.ts` — shared DOM registry; `$(id)` is the lookup for any element touched
  by more than one module.
- `messages.ts` and its `messages-*.ts` siblings, `tool-card.ts`,
  `reconcile.ts`, `smd-parser.ts` / `smd-renderer.ts` — the message-rendering
  pipeline.
- `actions/` — vibekit's action definitions plus `boot.ts` wiring; the framework
  itself is the published `@cplieger/actions` package.

For deeper client structure (tabs, editor/diff/conflict modes, shell, forges,
file browser) see `static-src/README.md` and the module headers.

## Invariants (do not break)

These rules exist because breaking them caused real bugs. Preserve them.

- **Server state is canonical.** Every mutation goes through `POST /api/command`,
  is persisted, and is echoed via SSE. The client renders nothing the server has
  not confirmed.
- **No optimistic local rendering of server state.** The client waits for the
  server's `message_appended` echo before showing a user bubble. This is what
  eliminates multi-device drift and the "vanishing message" class of bugs.
  (Action-level optimistic UI for local affordances is fine; inventing canonical
  chat state on the client is not.)
- **One JSON file per chat. No second store.** The directory listing is the
  index — there is no `index.json`, `sessions.json`, or migration layer. If
  state drifts, the answer is always "read the chat file."
- **Only `cmdDeleteChat` deletes a chat file.** Bridge exits, model switches,
  and restarts never delete. A live bridge always implies a live chat record.
- **Translate ACP events to domain events.** Never emit raw ACP to clients; do
  the translation in the `internal/translate/` handlers.
- **Use the `internal/api` response helpers.** Never hand-craft JSON error or
  success bodies (`http.Error` with a JSON string, `fmt.Fprint`,
  `w.Write([]byte(...))`). Use `Ok`, `WriteJSON`, `BadRequest`, `NotFound`,
  `Conflict`, `InternalError`, and the rest.
- **Logs are UTC.** `internal/logctl` installs the logger via `slogx.Setup`,
  whose `UTCTime` `ReplaceAttr` forces every record's timestamp to UTC, so the
  container needs no `TZ` and the binary embeds no `time/tzdata`.
- **Only `transport.ts` / `sw.ts` / `upload.ts` / `api-client.ts` call
  `fetch()`.** All other modules use the `api-client.ts` helpers.
- **Cross-module DOM access goes through `dom.ts` `$`.** Don't reach for
  `document.getElementById` for an element more than one module touches.
- **User-initiated mutations go through the actions framework** (see below).
- **Use the expandable-pill pattern for pill-row controls** (`pill-expand.ts`),
  not floating popups.

## User-initiated mutations go through the actions framework

Any button click, form submit, or keystroke that triggers an HTTP request or a
`transport.send` must go through the action framework in `static-src/actions/`.
The framework is the published `@cplieger/actions` package; vibekit's action
_definitions_ live in `static-src/actions/*.ts`, and `actions/index.ts`
re-exports the package surface.

See the [`@cplieger/actions`](https://github.com/cplieger/actions) package docs
for the full action-definition API and a worked `files.rename` example.

The framework handles, depending on the definition:

- Optimistic UI plus automatic rollback on failure (`defineAction` with
  `optimistic` / `rollback`).
- Toast on error with sensible defaults; success toasts are opt-in.
- Cancellation via `AbortController` and per-scope dispatch queuing.
- Retry with backoff for network-classified failures.
- An action log in the registry for devtools and loading-state binding.

Naming convention: `<area>.<verb>`, lowercase with underscores (`chat.delete`,
`files.rename`, `mcp.add_server`). Names are registry keys that tests grep, so
don't rename them casually.

A regression test, `actions/lint.test.ts`, scans the source tree and fails on
new write-shaped calls (`void`/`await apiPost`, `apiDelete`, `apiPutOrError`,
`transport.send`) outside the action files. If you are adding a legitimate
background poll or fire-and-forget cleanup, add the file's basename to
`BACKGROUND_ALLOWLIST` in that test with a one-line comment explaining why.
Otherwise, write an action.

## Toasts vs banners vs inline errors

| Surface             | Use for                                                                                                                         |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| Toast (transient)   | User-initiated mutation failures. Errors stick until dismissed.                                                                 |
| Banner (persistent) | Critical state the user must see across navigation (rate limits, model unavailable, MCP unhealthy). Lives in `banner-stack.ts`. |
| Inline form error   | Form validation. Surface near the field that failed.                                                                            |
| Log only            | Background polls, SSE bus errors, infrastructure exceptions, fire-and-forget cleanup.                                           |

Don't double-fire. If a banner is already up, set the action's `error` field to
`false` to suppress the toast.

## Local development

### Server

The Go server lives at the repo root. The compiled web UI is embedded via
`go:embed` from `static/`, which already contains the committed HTML, icons, and
`manifest.json`, so the server builds without first compiling the frontend:

```sh
go build ./...        # compile everything
go run .              # run the server (needs kiro-cli on PATH to do useful work)
```

The full application — Go server, kiro-cli download, and the on-demand tool
chain — is designed to run in the container. The image is built from the
multi-stage `Dockerfile`, and `compose.yaml` wires up the persistent `/config`
volume and exposes the UI on port `9847`.

### Frontend assets

The browser bundle is **produced during the Docker build, not committed**. The
builder stage runs `tsc` (the TS7 native compiler) over
`static-src/tsconfig.build.json` and `tsconfig.sw.json` to compile TypeScript
into `static/`, then concatenates the
per-feature CSS splits listed in `static-src/css/MANIFEST` into
`static/style.css`. The compiled outputs (`static/*.js`, `static/vendor/`,
`static/style.css`, and the mirrored subdirectories) are gitignored; only the
hand-written assets in `static/` are committed.

To iterate on `static-src/` locally, work through the package scripts (run from
`static-src/`). `tsc` (the TypeScript 7 native compiler) comes from the
`@typescript/native` devDependency (an npm alias for `typescript@7`); it and the
JavaScript devDependencies (vitest, eslint, prettier, stylelint, html-validate,
knip) all come from `npm install`:

```sh
cd static-src
npm test               # vitest --run (single pass)
npm run test:watch     # vitest watch mode
npm run typecheck      # tsc -project tsconfig.json (source)
npm run typecheck:tests # tsc -project tsconfig.test.json (tests)
```

## Running checks locally

The one-shot, full-battery option mirrors CI exactly (it runs the same reusable
workflow logic the GitHub `ci.yaml` does, including Go and frontend jobs):

```sh
bash ci-local.sh vibekit   # from a checkout of the cplieger/ci repo
```

The direct commands are the primary day-to-day path.

### Go

```sh
go build ./...             # compile
go test ./...              # unit + property tests
go test -race ./...        # race detector
golangci-lint run          # lint (config in .golangci.yaml)
golangci-lint fmt          # apply gofumpt + gci formatting
```

`golangci-lint run` reports unformatted files as issues, so the lint step also
enforces formatting; `golangci-lint fmt` is the fixer.

### Frontend (from `static-src/`)

```sh
npm run typecheck          # tsc -project tsconfig.json
npm run typecheck:tests    # tsc -project tsconfig.test.json
npm test                   # vitest --run
npm run lint:eslint        # eslint . (strict typed linting)
npm run lint:prettier      # prettier --check ../..
npm run lint:knip          # unused-export / dependency check
npx stryker run            # mutation testing (slow; config in stryker.config.json)
```

Mutation runs use their own vitest config (`vitest.stryker.config.ts`, a
raised test-timeout overlay on `vitest.config.ts`). Keep it free of bare
imports and out of the browser build: CI's import-map coverage check and
`tsconfig.build.json` both only exempt `vitest*.config.ts` shapes.

CSS is linted with stylelint (`.stylelintrc.json`) and HTML with html-validate
(`.htmlvalidate.json`); both ship as devDependencies and run in CI. Invoke them
directly with `npx stylelint` and `npx html-validate` when touching CSS or
markup. `npm run lint:fix` applies eslint `--fix` plus prettier `--write`.

## Testing conventions

Tests live beside the code they cover — standard for both ecosystems:

- **Go**: `foo.go` → `foo_test.go` in the same package. Property-based tests use
  `pgregory.net/rapid`. Run the race detector (`go test -race ./...`) on changes
  to concurrent code (the hub, bridge, chat store, checkpoints).
- **TypeScript**: `foo.ts` → `foo.test.ts`, co-located. The runner is vitest;
  property-based tests use `fast-check`. DOM-dependent tests opt into happy-dom
  with `// @vitest-environment happy-dom` at the file top. For action tests, mock
  `../toast.js`, dispatch, and assert against `getActionLog()`.

Add or update tests with every behaviour change, and make sure the relevant
checks above pass before opening a PR.

## Commits and pull requests

vibekit uses [Conventional Commits](https://www.conventionalcommits.org/);
git-cliff parses them to generate release notes and drive the version bump (see
`cliff.toml`). Write the subject as a public changelog line.

| Prefix                                                           | Effect                  |
| ---------------------------------------------------------------- | ----------------------- |
| `feat:`                                                          | new feature (Added)     |
| `fix:`                                                           | bug fix (Fixed)         |
| `sec:`                                                           | security fix (Security) |
| `refactor:` / `perf:`                                            | Changed                 |
| `chore:` `ci:` `docs:` `style:` `test:` `fuzz:` `lint:` `debug:` | no release              |
| `chore(deps):`                                                   | Dependencies (releases) |

The project is pre-1.0, so it stays within `0.x`: features bump the patch level
and breaking changes bump the minor — no automatic `1.0`.

Feature work goes on a dedicated branch named after the change (for example
`feat/actions` or `style/git-empty-states`). Changes land via a pull request
against `main`. Keep changes focused, run the checks
above, and let CI (`.github/workflows/ci.yaml`) confirm the full battery.

## Conduct and security

By participating you agree to abide by the organization
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report security vulnerabilities through the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md) —
never in a public issue.
