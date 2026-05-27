# Unified Reactive Primitives: vibekit + subflux alignment

Extract a shared reactive library used by both vibekit and subflux.
Take the best of each app's current implementation and apply to both.

## Primitives to include

### From subflux (adopt into vibekit)

1. **Typed per-key store** (`store.ts`)
   - `get<K>(key)` → `StoreMap[K]` (compile-time typed)
   - `set<K>(key, value)` → notifies only subscribers of that key
   - `subscribe<K>(key, callback)` → per-key granular subscription
   - Effect auto-tracking: `effect(() => { get("x"); get("y"); })`
     re-runs only when "x" or "y" change, not on any mutation
   - Source: `/workspace/subflux/internal/server/static-src/store.ts`

2. **`computed(fn)`** — derived values
   - Lazy evaluation, memoized, auto-invalidates when dependencies change
   - Source: `/workspace/subflux/internal/server/static-src/store.ts`

3. **`reconcileChildren(parent, newChildren)`** — structural tree-diff
   - Patches arbitrary DOM trees (not just flat keyed lists)
   - Handles attribute updates, text node changes, element reordering
   - Source: `/workspace/subflux/internal/server/static-src/dom.ts`

### From vibekit (adopt into subflux)

4. **`signal<T>(initial)`** — leaf-level reactive cell
   - For high-frequency per-entity updates (streaming text, tool status)
   - Lighter than a full store key; no string-based lookup overhead
   - Source: `/workspace/vibekit/web/static-src/signals.ts`

5. **`effect(fn)` with cleanup return**
   - `effect(() => { const x = sig.value; return () => cleanup(x); })`
   - Teardown runs before re-execution AND on disposal
   - Source: `/workspace/vibekit/web/static-src/signals.ts`

6. **`batch()` via MessageChannel** (React scheduler pattern)
   - Multiple synchronous mutations in the same tick coalesce into
     one subscriber notification on the next microtask
   - Callers don't need to wrap in `batch()` — it's automatic for
     streaming paths
   - Source: `/workspace/vibekit/web/static-src/signals.ts`

7. **`reconcile(parent, items, spec)`** — keyed-list reconciliation
   - `spec: { key, mount, update?, onRemove? }`
   - Identity-preserving: existing DOM elements survive across renders
   - Lifecycle hooks: mount (create), update (patch), onRemove (cleanup)
   - Source: `/workspace/vibekit/web/static-src/reconcile.ts`

## Architecture

```
lib/reactive/          (shared, vendored into both apps)
├── signal.ts          — signal<T>, effect (with cleanup), batch (MessageChannel)
├── store.ts           — typed per-key store, get/set/subscribe, computed
├── reconcile.ts       — keyed-list reconciliation (mount/update/onRemove)
├── reconcile-tree.ts  — structural tree-diff (reconcileChildren)
└── index.ts           — re-exports
```

Both apps vendor this as a local dependency (copy into their
`static-src/lib/` or use a workspace symlink). No npm publish; the
two apps share a git subtree or a simple copy-on-change workflow.

## Vibekit adoption plan

### Replace `signals.ts` with the shared lib

Current vibekit `signals.ts` (signal + effect + batch) becomes a
thin re-export of `lib/reactive/signal.ts`. No behavioral change;
just the import path moves.

### Replace the `version` counter with a typed per-key store

Current pattern:
```ts
export const version = signal(0);
function emit() { version.value = version.peek() + 1; }
// Every effect subscribes to version.value → re-runs on ANY mutation
```

New pattern:
```ts
// store.ts
export type StoreMap = {
  activeId: string;
  sessions: Session[];
  thinking: Map<string, boolean>;
  // ... every piece of state gets a typed key
};
// Effects auto-track: effect(() => { get("activeId"); }) only re-runs
// when activeId changes, not when sessions or thinking change.
```

This eliminates the remaining "version bumps everything" pattern.
The per-entity signals (streamingTextSig, toolCallSig, crewSig)
stay as-is — they're leaf-level signals for high-frequency updates
that don't belong in the store.

### Add `computed()` for derived state

Examples:
- `const activeSession = computed(() => get("sessions").find(s => s.id === get("activeId")))`
- `const isThinking = computed(() => get("thinking").get(get("activeId")) ?? false)`
- `const contextPct = computed(() => activeSession.value?.usage.context_pct ?? 0)`

Currently these are computed inside effects (redundant re-computation
on every render). `computed()` memoizes and only re-evaluates when
its dependencies actually change.

### Keep `reconcile()` as-is

Already in the shared lib shape. Just move the file.

## Subflux adoption plan

### Replace `store.ts` internals with the shared lib

Subflux's store already has the right API shape (`get`, `set`,
`subscribe`, `effect`, `computed`, `batch`). The shared lib is
essentially subflux's store + vibekit's MessageChannel batch +
vibekit's effect-cleanup. Subflux's store.ts becomes a thin wrapper
that re-exports from the shared lib with its existing `StoreMap` type.

### Add `reconcile()` for keyed lists

Subflux currently uses `reconcileChildren` (tree-diff) for
everything. For identity-preserving lists (coverage table rows,
history entries, scan results), `reconcile()` with `onRemove` hooks
is cleaner:

```ts
reconcile(coverageList, items, {
  key: (item) => item.id,
  mount: (item) => buildCoverageRow(item),
  update: (el, item) => updateCoverageRow(el, item),
  onRemove: (el) => { /* cleanup timers, observers */ },
});
```

### Adopt MessageChannel batch

Subflux's current `batch()` is synchronous-defer (notifications fire
when the batch function returns). The shared lib's MessageChannel
batch means SSE event bursts (`scan:done` + `coverage` + `notify`
arriving in the same tick) automatically coalesce without the event
handler needing to wrap in `batch()`.

### Adopt effect cleanup

Subflux's current `effect()` doesn't return a cleanup function.
Effects that set up intervals or observers currently track their own
teardown. With cleanup:

```ts
effect(() => {
  const id = setInterval(poll, 5000);
  return () => clearInterval(id);
});
```

## Build order

1. **Extract `lib/reactive/`** from vibekit's signals.ts + reconcile.ts
   + subflux's store.ts + dom.ts reconcileChildren. Merge into one
   coherent module with tests.

2. **Adopt in vibekit** — replace signals.ts import, replace version
   counter with typed store, add computed() where beneficial. Run
   full test suite.

3. **Adopt in subflux** — replace store.ts internals, add reconcile()
   for lists, adopt MessageChannel batch + effect cleanup. Run full
   test suite.

4. **Verify both apps** — typecheck + tests + manual smoke.

## Decisions

- **No npm package.** Both apps are in the same workspace; a vendored
  copy (or git subtree) is simpler than publishing.
- **No breaking API changes in subflux.** Its store API (`get`, `set`,
  `subscribe`, `effect`, `computed`, `batch`) stays identical; only
  the internals change (MessageChannel batch, cleanup support).
- **Vibekit's per-entity signals stay.** They're orthogonal to the
  store — used for high-frequency leaf updates (streaming chunks,
  tool status) that don't belong as store keys.
- **`reconcileChildren` (tree-diff) stays alongside `reconcile`
  (keyed-list).** Different tools for different jobs. Tree-diff for
  arbitrary DOM updates; keyed-list for identity-preserving lists.

## Files to read before starting

- `/workspace/vibekit/web/static-src/signals.ts` (signal + effect + batch)
- `/workspace/vibekit/web/static-src/reconcile.ts` (keyed-list)
- `/workspace/subflux/internal/server/static-src/store.ts` (typed store + computed)
- `/workspace/subflux/internal/server/static-src/store.test.ts` (store contracts)
- `/workspace/subflux/internal/server/static-src/store.property.test.ts` (invariants)
- `/workspace/subflux/internal/server/static-src/dom.ts` (reconcileChildren)
