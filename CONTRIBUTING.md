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

- `main.go` / `embed.go`: the composition root. Load config, construct the
  store, hub, bridge, and push service, register HTTP routes, embed the
  compiled static assets. Wiring only.
- `internal/api/`: interface contracts (`ChatStore`, `Hub`, `ACPBridge`,
  `PushService`) plus the domain types (chat, message, tool call, plan, usage).
  Every other package depends only on this one. Includes `httputil.go` response
  helpers; use them, never hand-craft JSON.
- `internal/chat/`: persistence, one JSON file per chat, written atomically.
  `Mutate` is the only write path.
- `internal/hub/`: command dispatch, SSE broadcast, ACP-to-domain event
  translation, bridge buffer aggregation, and the global PTY shell.
- `internal/bridge/`: `kiro-cli acp` subprocess lifecycle, capability
  handshake, and the filesystem read/write handlers. The binary it runs is
  resolved once per bridge, from the install manager, so a version switch reaches
  the next chat instead of being frozen at boot.
- `internal/composition/kirocli.go`: vibekit's deployment of
  [pinstall](https://github.com/cplieger/pinstall), the digest-pinned install
  library, with its ready-made `pinstall/kirocli` release profile. The library
  downloads the pinned archive, verifies its per-arch SHA-256, installs into
  `<tools dir>/kiro-cli-versions/<version>/` behind a `.complete` sentinel written
  last, selects the active version (re-probing `--version` before trusting any
  directory), re-asserts the settings the pin depends on, and keeps exactly one
  predecessor. What this file owns is the deployment: the pins, the tools tree, the
  required/optional artifact split, the eight experimental settings, the
  trusted-writer declaration, and the purge data for the layout vibekit's own
  shell installer used to promote into `$TOOLS/bin`. The trusted-writer
  declaration is the one that can withhold readiness: the library refuses to
  install into a tree another identity can write, and reads access-control lists
  to find that out. `TrustedUIDs` carries the identities a deployment vouches
  for, and it is deliberately NOT a compiled-in value — it comes from
  `WT_TRUSTED_INSTALL_UIDS` (parsed by `parseTrustedInstallUIDs` in
  `internal/composition/config.go`), because only the deployment knows which
  account on its volume already holds at least this process's privilege. The
  image ships it unset, which leaves the check fully enforcing. `Untrusted`
  stays deliberately unset here — vibekit has no hardening pass that could make
  that observation.

  Nothing in this file exits the process: every failure degrades readiness
  instead, so the UI and the `docker exec` repair path survive a broken install.
  `entrypoint.sh` supplies only the three Renovate-pinned literals.
- `internal/translate/`: ACP notification handlers that turn raw `kiro-cli`
  events into vibekit domain events.
- `internal/command/`: handlers for each `POST /api/command` type.
- `internal/checkpoint/`: content-addressed file-snapshot store with a
  two-phase atomic restore.
- `internal/permissions/`, `internal/forges/`, `internal/git/`,
  `internal/mcp/`, `internal/push/`, `internal/auth/`,
  `internal/server/`: feature subsystems (tool-approval policy, forge CLI
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
  other small packages: focused helpers.

Server dependencies flow one direction: composition root → hub → API contracts
→ feature packages. No reverse imports.

### Client (`static-src/`, compiled to `static/`)

- `app.ts`: the composition root. It is the only module that imports and wires
  everything; nothing imports from it.
- `transport.ts`, `sw.ts`, `upload.ts`, `api-client.ts`: the only modules
  allowed to call `fetch()` directly. Everything else goes through
  `api-client.ts` (`apiGet` / `apiPost` / `apiDelete` and typed variants).
- `store.ts` (+ `store-load.ts`, `store-signals.ts`): the chat-state singleton,
  a projection of server state.
- `bus.ts`: cross-module event bus that breaks import cycles.
- `dom.ts`: shared DOM registry; `$(id)` is the lookup for any element touched
  by more than one module.
- `messages.ts` and its `messages-*.ts` siblings, `tool-card.ts`,
  `reconcile.ts`, `smd-parser.ts` / `smd-renderer.ts`: the message-rendering
  pipeline.
- `actions/`: vibekit's action definitions plus `boot.ts` wiring; the framework
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
  index; there is no `index.json`, `sessions.json`, or migration layer. If
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

`go run .` installs nothing: with no `KIRO_CLI_VERSION` in the environment the
manager stands down, kiro-cli is resolved by bare name, and `/api/health` reports
only that the listener is up. The install runs when the entrypoint exports the
pins, so a first boot in the container answers `503` with a reason naming the
state while the archive downloads, then flips to healthy on its own.

#### Exercising the managed install without a 528 MB download

No env var points vibekit at a binary you picked; the install manager is the only
thing that resolves kiro-cli's path. What the manager does do is adopt a version
directory that is already complete on disk, downloading nothing, and that's the
seam to use locally and in tests. Populate it yourself:

```text
$VIBEKIT_TOOLS_DIR/kiro-cli-versions/<version>/
├── kiro-cli      # executable; must answer `--version` with <version>
└── .complete     # the sentinel; written LAST, contains <version>
```

```sh
export VIBEKIT_TOOLS_DIR=/tmp/vibekit-tools KIRO_CLI_VERSION=2.14.2
# Both digests are validated when the manager is CONSTRUCTED, before it knows
# whether it has anything to download, so they must be 64 lowercase hex
# characters each — but nothing is fetched here, so the values are arbitrary.
export KIRO_CLI_SHA256=0000000000000000000000000000000000000000000000000000000000000000
export KIRO_CLI_SHA256_ARM64=0000000000000000000000000000000000000000000000000000000000000000
V="$VIBEKIT_TOOLS_DIR/kiro-cli-versions/$KIRO_CLI_VERSION"
mkdir -p "$V"
cp /path/to/kiro-cli /path/to/kiro-cli-chat "$V/"
printf '%s\n' "$KIRO_CLI_VERSION" >"$V/.complete"
go run .
```

Both `kiro-cli` and `kiro-cli-chat` are required. kiro-cli is a multi-call binary
and `kiro-cli acp` re-execs the chat sidecar, resolved by a plain PATH search:

```console
$ kiro-cli acp --help
Usage: kiro-cli-chat acp [OPTIONS]
$ env -i PATH=<a directory holding ONLY kiro-cli> kiro-cli acp --help
error: No such file or directory (os error 2)
```

So a directory without the sidecar is not a usable install, even though
`kiro-cli --version` (answered by the main binary) succeeds against it.
`kiro-cli-term` is optional and merely warns when absent.
`.complete` is what makes the directory a selection candidate, and the two
per-boot gates still run against whatever you put there: `kiro-cli --version` must
print the directory's own name, and `app.disableAutoupdates=true` must be
assertable through `kiro-cli settings` or readiness is withheld. A shell script
answering both is enough for a wiring check;
`internal/composition/kirocli_test.go` builds exactly that fake dispatcher.

The full application (Go server, kiro-cli download, and the on-demand tool
chain) is designed to run in the container. The image is built from the
multi-stage `Dockerfile`, and `compose.yaml` wires up the persistent `/config`
volume and exposes the UI on port `9847`.

### Frontend assets

The browser bundle is **produced during the Docker build, not committed**. The
builder stage runs `tsc` (the TS7 native compiler) with `--noEmit` as the type
gate over `static-src/tsconfig.build.json` and `tsconfig.sw.json`, then runs
`go run ./cmd/bundle` (esbuild via its Go API; no Node, no bundler binary),
which bundles `static-src/app.ts` into `static/app.js` plus hashed lazy chunks
under `static/chunks/` (the dynamic `import()` sites), bundles `sw.ts` into
`static/sw.js`, and concatenates the CSS manifests
(`@cplieger/web-terminal-ui`'s `MANIFEST.touch`, then `static-src/css/MANIFEST`)
into `static/style.css`. Serving compression is the server's job: it gzips
assets at startup, so the bundle writes no precompressed `.gz` siblings. The
`@cplieger/*` library sources are bundled in, so nothing is
served from `/vendor/` and the page carries no importmap. Those are all
first-party: the browser bundle has no third-party JavaScript in it. All bundle outputs
are gitignored; only the hand-written assets in `static/` are committed.

To produce a servable `static/` locally (for `go build` + a local run), use
the same two steps from the repo root after `npm install` in `static-src/`:

```sh
cd static-src && npm run typecheck -- --noEmit && cd ..
go run ./cmd/bundle
```

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

### Boot path

```sh
bash tests/shell/run.sh    # entrypoint.sh unit tests
shellcheck -S warning entrypoint.sh
shfmt -i 2 -ci -bn -d entrypoint.sh
```

The suite runs the real functions out of `entrypoint.sh` against temp
directories, covering the branches a booting image never takes: a `/config` it
cannot create, the agent-runtime pruner's refusals, and the pins the Go install
manager reads. Add a case by extracting the function and confirming the new
assertion fails against a mutated `/tmp` copy
(`ENTRYPOINT=/tmp/mut.sh bash tests/shell/<x>_test.sh`), never by editing
`entrypoint.sh` in place. `lib.sh` and `harness_test.sh` are synced from
`cplieger/ci`; change them there.

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
imports and out of the browser build: `tsconfig.build.json` only exempts
`vitest*.config.ts` shapes.

CSS is linted with stylelint (`.stylelintrc.json`) and HTML with html-validate
(`.htmlvalidate.json`); both ship as devDependencies and run in CI. Invoke them
directly with `npx stylelint` and `npx html-validate` when touching CSS or
markup. `npm run lint:fix` applies eslint `--fix` plus prettier `--write`.

## Testing conventions

Tests live beside the code they cover, standard for both ecosystems:

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
and breaking changes bump the minor; there is no automatic `1.0`.

Feature work goes on a dedicated branch named after the change (for example
`feat/actions` or `style/git-empty-states`). Changes land via a pull request
against `main`. Keep changes focused, run the checks
above, and let CI (`.github/workflows/ci.yaml`) confirm the full battery.

## Conduct and security

By participating you agree to abide by the organization
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report security vulnerabilities through the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md),
never in a public issue.
