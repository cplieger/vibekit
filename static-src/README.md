# web/static-src

TypeScript source for the Vibekit web UI. Compiled to static assets
served by the Go backend.

## Structure

| Path        | Purpose                                                       |
| ----------- | ------------------------------------------------------------- |
| `actions/`  | Declarative action framework (mutations, retry, scope, toast) |
| `handlers/` | SSE message handlers (turn, pending, system)                  |
| `wire/`     | Generated protocol decoders and types                         |
| `css/`      | Stylesheets (numbered for load order)                         |
| `*.ts`      | UI modules (DOM, state, rendering)                            |
| `*.test.ts` | Co-located unit tests                                         |

## Development

```sh
npm test              # single pass (vitest --run)
npm run test:watch    # watch mode
npm run test:coverage # with V8 coverage
npm run typecheck     # tsc type-check (source)
npm run typecheck:tests # tsc type-check (tests)
```

## Test environment

Two vitest projects, and the DEFAULT is the browser:

- **browser** — headless Chromium via `@vitest/browser-playwright`. Every test
  runs here unless its filename opts out. No `environment` option and no
  per-file pragma: Browser Mode is a runner, not an environment.
- **node** — the `*.node.test.ts` suffix, for a test that needs genuine Node
  capabilities (spawning a process, walking a directory) or needs the DOM to be
  ABSENT. The reason goes in the stem, the placement in the suffix:
  `attention-no-dom.node.test.ts`.
- Reading a shipped file uses a Vite `?raw` import rather than `node:fs`, which
  is what lets a suite that checks source text stay in the browser project.
- Property-based tests use `fast-check` with strict setup (`fc-strict-setup.ts`)
- All mocks auto-reset between tests (`restoreMocks: true`)

The browser is installed once with `npx --no-install playwright install chromium`;
CI installs it from the `@vitest/browser-playwright` devDependency.

## Key conventions

- `requireAssertions: true` — every test must call `expect()`
- Browser Mode isolates per test FILE; the node project sets `isolate: true`
- `vi.resetModules()` does NOT re-evaluate a module in the browser (the module
  map is URL-keyed). A test that needs a fresh module graph busts the specifier:
  ``await import(/* @vite-ignore */ `./x.ts?boot=${n}`)`` — keep the `.ts`.
- A `vi.mock` factory must name every export the graph imports, because the
  browser links ESM for real, and it runs ONCE: a per-test `vi.doMock` cannot
  replace a mock that is already cached, so per-test values reach a factory
  through module state rather than a closure.
- Test files co-located with source: `foo.ts` → `foo.test.ts`
- Actions use `defineAction` / `apiAction` / `transportAction` factories
