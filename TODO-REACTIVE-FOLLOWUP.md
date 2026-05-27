# Reactive Lib Follow-up: Concrete Improvements

Post-adoption improvements for both apps using the shared `lib/reactive/`.

## 1. Add error recovery to shared lib effect()

**Problem:** If an effect's callback throws, the error propagates unhandled.
The effect's deps are cleared (it unsubscribed before re-running) but never
re-subscribes, so it's permanently dead. Subflux's old store had try/catch.

**Impact:** Silent UI breakage — a panel stops updating with no error visible.

**Solution:** Wrap the user function in try/catch inside `execute()`. Log the
error but still complete the tracking phase so deps are recorded. The effect
stays alive and will re-run on next dependency change.

```ts
// In signal.ts effect's execute():
execute() {
  if (disposed) return;
  for (const depSet of sub.deps) depSet.delete(sub);
  sub.deps.clear();
  if (cleanup) { cleanup(); cleanup = undefined; }
  const prev = tracking;
  tracking = sub;
  try { cleanup = fn(); }
  catch (e) { console.error("effect error:", e); }
  finally { tracking = prev; }
}
```

Same pattern in store.ts `storeEffect`'s `run()` — already has try/catch but
should also handle cleanup errors.

**Files:** `lib/reactive/signal.ts`, `lib/reactive/store.ts` (both copies)
**Effort:** 5 minutes
**Risk:** None — strictly more resilient

---

## 2. Deduplicate reconcileChildren/patch from subflux's dom.ts

**Problem:** `dom.ts` contains ~80 lines of `reconcileChildren`, `patch`,
`canPatch`, `nodeKey`, `patchAttrs` that are now duplicated in
`lib/reactive/reconcile-tree.ts`.

**Impact:** Two implementations to maintain. If one gets a bug fix, the other
doesn't.

**Solution:** Delete the 5 internal functions from `dom.ts`. Re-export `patch`
from the shared lib. Keep all other `dom.ts` exports (el, icon, $, dialog, etc.)
unchanged.

```ts
// In dom.ts, replace the patch/reconcileChildren block with:
export { patch } from './lib/reactive/reconcile-tree.js';
```

**Caveat:** subflux's `patchAttrs` also handles `on*` event handler properties
via a WeakMap (`handlerKeysMap`). The shared lib's `patchAttrs` does NOT handle
this — it only patches attributes. Two options:

A) Keep subflux's `patchAttrs` as a local override and only import
   `reconcileChildren` from the shared lib, wrapping it with the handler logic.

B) Extend the shared lib's `reconcile-tree.ts` to support an `onPatchAttrs`
   hook that subflux can use for handler tracking.

**Recommendation:** Option A. The handler tracking is subflux-specific (vibekit
doesn't use `on*` properties on elements). Keep the shared lib minimal.

Concrete approach:
- Move `reconcileChildren` import from shared lib
- Keep local `patchAttrs` (with handler WeakMap) in dom.ts
- Override the shared lib's internal `patchAttrs` by NOT importing `patch`
  directly — instead import `reconcileChildren` and build a local `patch`
  that uses the local `patchAttrs`.

Actually simplest: just leave dom.ts as-is. The duplication is 80 lines and
the implementations diverge (handler tracking). The shared lib serves vibekit;
subflux's dom.ts serves subflux. They share the *concept* but not the exact
code. **Downgrade to "won't fix" — the divergence is intentional.**

**Revised verdict:** Skip. The handler WeakMap makes them genuinely different.

---

## 3. Convert subflux coverage table to reconcile()

**Problem:** `paged-list.ts` calls `patch(container, frag)` which does a full
tree-diff on every reload/loadMore. For the coverage table (potentially hundreds
of rows), this means the entire `<tbody>` is rebuilt — all event listeners
re-attached, all DOM nodes recreated.

**Impact:** Visible flicker on large libraries. Scroll position loss on
"Show more" (currently hacked with `window.scrollTo`).

**Solution:** Replace `renderItems` callback pattern with `reconcile()` on the
tbody. Each row is keyed by media ID (`coverageMediaId(item)`).

```ts
// In coverage.ts, replace renderCoverageItems with:
import { reconcile } from './lib/reactive/reconcile.js';

function renderCoverageTable(container: HTMLElement, items: CoverageItem[]): void {
  // Ensure table structure exists (thead created once).
  let table = container.querySelector('table');
  if (!table) {
    table = el('table', null,
      el('thead', null, el('tr', null, ...theadCells)),
    );
    table.appendChild(el('tbody'));
    container.replaceChildren(table);
  }
  const tbody = table.querySelector('tbody')!;
  reconcile(tbody, items, {
    key: (item) => coverageMediaId(item),
    mount: (item) => buildCoverageRow(item),
    update: (row, item) => updateCoverageRow(row, item),
  });
}
```

**Requires:** Extract `buildCoverageRow` (the current tbody.appendChild block)
into a standalone function. Add `updateCoverageRow` that patches badges and
action button state without rebuilding the row.

**Effort:** 30 minutes
**Risk:** Low — coverage table is well-tested visually. The "Show more" button
needs to move outside the reconciled tbody (append after table).

---

## 4. Subflux: adopt effect() for reactive rendering

**Problem:** Subflux has only 1 `subscribe()` call and 2 `computed()` calls in
production code. The `effect()` primitive (with auto-tracking and cleanup) is
available but unused. Several modules manually wire up event listeners and
teardown logic that `effect()` would handle automatically.

**Impact:** More boilerplate, manual unsub tracking, risk of leaks.

**Candidates for conversion:**

a) `app.ts:83` — `store.subscribe('needsRefresh', ...)` → effect that reads
   `get('needsRefresh')` and calls the refresh logic. Gains: auto-cleanup if
   the module ever needs teardown.

b) `events.ts` — SSE event handlers that call `store.set(...)`. These are fine
   as-is (they're event sources, not reactive consumers).

c) `status.ts` — renders status popup based on multiple pieces of state.
   Currently imperatively called from event handlers. Could be an effect that
   reads relevant store keys and re-renders. But status.ts is already clean.

**Verdict:** The opportunity is small. Subflux's architecture is event-driven
(SSE → store.set → subscriber/computed → render). It doesn't have the "many
effects watching one signal" pattern that vibekit has. The single `subscribe`
call is fine. **Low priority — adopt effect() opportunistically in new code,
don't refactor existing code.**

---

## 5. Vibekit: split version signal into 2-3 domain signals

**Problem:** 7 effects subscribe to `version.value`. Every `emit()` (33 call
sites) wakes ALL of them. Most effects only care about a subset:
- `messages.ts` cares about: active session messages, thinking state
- `tabs.ts` cares about: session list, active ID, names
- `app.ts` cares about: active session's available_models
- `auto-approve.ts` cares about: active session's auto_approve_crew
- `supervised-pill.ts` cares about: active session's supervised_mode
- `chat.ts` cares about: active session's thinking + queue
- `task-list.ts` cares about: active session's messages (task events)

**Impact:** On a streaming chunk that only updates message content, ALL 7
effects re-run. The per-entity signals (streamingTextSig, toolCallSig) already
bypass this for the hot path, but metadata mutations (setThinking, setName,
upsertHeader) still wake everything.

**Solution:** Split into 3 signals:

```ts
export const sessionsVersion = signal(0);  // list changes, add/remove/reorder
export const activeVersion = signal(0);    // active session metadata changes
export const messagesVersion = signal(0);  // message list changes (append, upsert)
```

Each `emit()` call site bumps the appropriate signal(s). Effects subscribe to
only what they need:
- `tabs.ts` → `sessionsVersion` + `activeVersion`
- `messages.ts` → `messagesVersion` + `activeVersion`
- `app.ts` → `activeVersion`
- etc.

**Effort:** 1 hour (mechanical: categorize each emit() call, update effects)
**Risk:** Medium — wrong categorization causes missed renders. Mitigated by
tests (1704 existing tests catch regressions).

**Alternative:** Keep single `version` but add a `bitmask` parameter:
```ts
const SESSIONS = 1, ACTIVE = 2, MESSAGES = 4;
function emit(mask: number) { ... }
// Effects filter: effect(() => { if (!(version.mask & MESSAGES)) return; ... })
```
Uglier but zero-risk refactor (effects that don't filter still work).

**Recommendation:** Do the 3-signal split. It's clean, type-safe, and the
categorization is obvious from the function names (setThinking → activeVersion,
appendMessage → messagesVersion, removeChat → sessionsVersion).

---

## 6. Subflux bus events: LoadHistory → store key

**Problem:** `BusEvent.LoadHistory` is emitted by router.ts when navigating to
the history page. history.ts subscribes and calls `loadHistory()`. This is a
"please load data" command, not a state-change notification.

**Impact:** Minimal. The bus is the right tool for commands/actions. This is
NOT a case of "data changed" masquerading as an event.

**Verdict:** After closer inspection, ALL of subflux's bus events are
navigation/action commands (OpenSeries, OpenMovie, ScanSeries, NavRoute, etc.),
not state-change notifications. The bus and store have clean separation already.
**No change needed.**

---

## 7. Vibekit store.ts: reduce mutation function count

**Problem:** store.ts is 400+ lines with 30+ exported mutation functions. Each
is 3-8 lines of "get session, guard, mutate, emit()". High surface area.

**Impact:** Cognitive load. New contributors don't know which function to call.
Some functions are near-duplicates (setModel vs setName — same pattern).

**Solution:** NOT a refactor priority. The functions are well-named, tested,
and each serves a specific SSE handler or action. Consolidating them into a
generic `updateSession(id, patch)` would lose type safety and make call sites
less readable. **Leave as-is.**

---

## Final TODO (actionable items only)

### Do now (this session)

1. **Error recovery in shared lib effect()** — add try/catch in both
   signal.ts and store.ts effect implementations. 5 min.

### Do next session

2. **Subflux coverage table → reconcile()** — extract buildCoverageRow,
   add updateCoverageRow, wire reconcile() in place of patch(). 30 min.

3. **Vibekit 3-signal split** — replace single `version` with
   `sessionsVersion` / `activeVersion` / `messagesVersion`. Categorize
   33 emit() sites. Update 7 effects. 1 hour.

### Skip (evaluated, not worth it)

- dom.ts dedup (handler WeakMap divergence makes them genuinely different)
- Subflux effect() adoption (architecture doesn't benefit; event-driven is fine)
- Bus→store migration (bus events are commands, not state — correct as-is)
- Store mutation consolidation (current API is clear and well-tested)

---

## Subflux Deep Audit: All reconcile() / effect() / signal() Opportunities

### S1. Coverage table rows → reconcile()

**Location:** `coverage.ts` → `renderCoverageItems()` (line 208)
**Current:** Builds entire `<table>` from scratch via DocumentFragment on every
`paged-list` reload/loadMore. All rows destroyed and recreated.
**Key available:** `coverageMediaId(item)` — unique per media item.
**Opportunity:** `reconcile(tbody, items, { key: coverageMediaId, mount: buildCoverageRow })`.
Existing rows survive across "Show more" appends and SSE-triggered refreshes.
**Benefit:** No flicker on refresh. Scroll position preserved naturally (no
`window.scrollTo` hack). Badge updates via `update` callback instead of the
separate `patchCoverageBadge` function.
**Effort:** 30 min. Extract `buildCoverageRow` from the loop body. The "Show
more" button moves outside the reconciled tbody.

### S2. History table rows → reconcile()

**Location:** `history.ts` → `renderItems()` (line 67)
**Current:** Full table rebuild on every page load / "Show more".
**Key available:** Each history entry has a unique composite key
(`${entry.media_id}-${entry.media_imported}`).
**Opportunity:** Same pattern as S1. `reconcile(tbody, items, { key, mount: buildHistoryRow })`.
**Benefit:** "Show more" appends without rebuilding existing rows. Filter
changes only remove/add the diff.
**Effort:** 20 min.

### S3. Episode rows in series detail → reconcile()

**Location:** `detail.ts` → `renderSeriesDetail()` (line 269)
**Current:** Builds all season sections + episode rows into a fragment, then
`patch(out, frag)` replaces the entire content area.
**Key available:** `tvdbMediaId(series.tvdb_id, sg.season, ep.episode)`.
**Opportunity:** `reconcile(tbody, episodes, { key: mediaId, mount: buildEpisodeRow, update: updateEpisodeRow })`.
**Benefit:** After a scan completes and SSE triggers refresh, only the changed
episode badges update — the rest of the DOM stays put. Currently the entire
detail view rebuilds (hundreds of rows for long-running shows).
**Caveat:** Season headers are interspersed with episode rows. Either use a
flat list with season-header items (keyed by `season-${n}`) or reconcile
per-season tbody.
**Effort:** 45 min (more complex due to season grouping).

### S4. Search results → reconcile()

**Location:** `search.ts` → `renderPopupResults()` (line 270)
**Current:** Full rebuild of result rows on every search.
**Key available:** `${result.provider}-${result.release_name}` (unique per result).
**Opportunity:** `reconcile(resultsContainer, results, { key, mount: buildResultRow })`.
**Benefit:** Minimal — search results don't update in place. The popup is
destroyed on close. **Skip unless download-in-progress state needs live update.**
**Verdict:** Skip. One-shot render, no incremental updates.

### S5. Files table → reconcile()

**Location:** `files.ts` → `renderFiles()` (line 106)
**Current:** Full table rebuild after every delete operation.
**Key available:** `${f.media_id}-${f.language}-${f.source}` (unique per file).
**Opportunity:** `reconcile(tbody, data, { key, mount: buildFileRow, onRemove: animateOut })`.
**Benefit:** After deleting a file, only that row animates out. Currently the
entire table rebuilds (flash of empty → repopulated).
**Effort:** 20 min.

### S6. Security lists (passkeys, API keys) → reconcile()

**Location:** `security.ts` → `buildPasskeysSection()` (line 350),
`buildAPIKeysSection()` (line 507)
**Current:** Full list rebuild on every open of the security dialog.
**Key available:** `pk.id` for passkeys, `key.id` for API keys.
**Opportunity:** `reconcile(list, items, { key: i => i.id, mount: passkeyRow })`.
**Benefit:** After registering/deleting a passkey, only the diff applies.
Currently the entire dialog body is rebuilt.
**Effort:** 15 min.
**Caveat:** These lists are small (typically 1-5 items). The benefit is
marginal. **Low priority.**

### S7. Status popup activity list → reconcile()

**Location:** `status.ts` → `buildPopupContent()` (line 299)
**Current:** Full popup rebuild on every poll (5s interval when open).
**Key available:** `activity.id` for activities, `alert.id` for alerts.
**Opportunity:** `reconcile(popupContainer, items, { key, mount, update })`.
**Benefit:** Live timers (`updateLiveTimers`) currently walk all `.pop-item`
elements by class. With reconcile, each activity row could have its own timer
signal that updates just the time text. Eliminates the full popup rebuild on
every 5s poll.
**Effort:** 30 min.
**Priority:** Medium — the popup is small but rebuilds frequently.

### S8. Notification toast stack → signal()

**Location:** `notify.ts`
**Current:** Imperative DOM management with manual queue, dismiss animations,
and MAX_VISIBLE limit.
**Opportunity:** Model the toast queue as a `signal<Toast[]>()`. An effect
reconciles the visible toasts. Auto-dismiss timers become effect cleanups.
**Benefit:** Cleaner code, automatic cleanup on page teardown.
**Verdict:** Skip. The current implementation is 90 lines, well-tested, and
the imperative approach is actually simpler for animations (CSS animationend
events don't compose well with reactive rendering).

### S9. Config form fields → effect() with cleanup

**Location:** `config.ts` → `renderConfigForm()` (line 197)
**Current:** Builds form fields imperatively, attaches change listeners manually.
No cleanup on re-render (the dialog is destroyed/rebuilt).
**Opportunity:** Use `effect()` to bind field validation state to store keys.
When config changes externally (e.g. reset), the form auto-updates.
**Benefit:** Minimal — the config dialog is modal and short-lived.
**Verdict:** Skip. Not worth the refactor for a modal form.

### S10. paged-list.ts → signal-driven state

**Location:** `paged-list.ts` (114 lines)
**Current:** Internal `items`, `hasMore`, `loading` state managed imperatively.
`render()` called manually after each state change.
**Opportunity:** Replace with signals:
```ts
const items = signal<T[]>([]);
const hasMore = signal(false);
const loading = signal(false);
effect(() => { renderList(items.value, hasMore.value, loading.value); });
```
**Benefit:** Eliminates manual `render()` calls. State changes automatically
trigger re-render. Loading state could drive button disabled state reactively.
**Effort:** 15 min.
**Priority:** Low — the module is small and correct as-is.

### S11. SSE reconnect state → signal()

**Location:** `events.ts` — `reconnectAttempt`, `reconnectTimer`, `eventSource`
**Current:** Module-level mutable variables. No reactive consumers.
**Opportunity:** Could expose connection state as a signal for a "disconnected"
banner. Currently there's no UI feedback when SSE drops.
**Benefit:** User sees "Reconnecting..." when the connection is lost.
**Effort:** 10 min for the signal, 20 min for the UI banner.
**Priority:** Nice-to-have. Not a primitive adoption issue.

---

## Revised Priority List

### Do now (done ✓)
1. ✓ Error recovery in shared lib effect()

### Do next (high value, low risk)
2. **S1: Coverage table → reconcile()** — biggest list, most frequent updates
3. **S5: Files table → reconcile()** — delete animation opportunity
4. **S2: History table → reconcile()** — "Show more" without rebuild

### Do later (medium value)
5. **S3: Episode rows → reconcile()** — complex (season grouping) but high row count
6. **S7: Status popup → reconcile()** — frequent rebuilds, small DOM
7. **S6: Security lists → reconcile()** — same code volume, gains delete animation + rename-in-place
8. **Vibekit 3-signal split** — reduces wasted re-renders

### Skip (evaluated, not worth it)
- S4: Search results (one-shot render, no updates)
- S8: Notification toasts (imperative is simpler for animations)
- S9: Config form (modal, short-lived)
- S10: paged-list signals (correct as-is, small module)
- S11: SSE reconnect signal (nice-to-have, not a primitive issue)
- dom.ts dedup (handler WeakMap divergence)
- Bus→store migration (bus events are commands)
- Store mutation consolidation (API is clear)
