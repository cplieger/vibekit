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

- Default: Node (pure functions, no DOM overhead)
- DOM tests: add `// @vitest-environment happy-dom` at file top
- Property-based tests use `fast-check` with strict setup (`fc-strict-setup.ts`)
- All mocks auto-reset between tests (`restoreMocks: true`)

## Key conventions

- `requireAssertions: true` — every test must call `expect()`
- `isolate: true` — each test file gets its own module graph
- Test files co-located with source: `foo.ts` → `foo.test.ts`
- Actions use `defineAction` / `apiAction` / `transportAction` factories
