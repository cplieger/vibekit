# actions/

A declarative framework for user-initiated mutations. Wraps the
imperative raw fetch with api-client helpers / `transport.send` pattern with a single
contract: every action declares its name, request, optional
optimistic UI, optional rollback, and toast wiring up front. The
dispatcher handles the lifecycle.

## When to use

- **Use an action** for any mutation triggered by the user
  (button click, form submit, keystroke). Saves, deletes, sends,
  uploads, toggles.
- **Don't use an action** for background polls (use raw fetch with
  api-client helpers), SSE bus handlers, or read-only fetches
  that auto-recover.
  - *Exception:* read-only fetches that benefit from dedupe +
    auto-retry + cancellation may use `defineAction`/`apiAction`
    with `error: false`. See `kiro-config.ts`, `retention.ts`,
    and `commands-menu.ts` (slash-command options) for examples.

## The three factories

### `apiAction` — the 90% case

For any mutation that's "POST/PUT/DELETE/PATCH this URL, surface
the server error":

```ts
import { apiAction } from "./index.js";

export const deleteFile = apiAction<string, void>({
  name: "files.delete",
  request: (path) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "delete", path },
  }),
  error: "Couldn't delete",
});

// Dispatch site:
const ok = await deleteFile.dispatch(somePath);
if (ok === null) return;  // toast already fired
```

**When NOT to use `apiAction`:** when the response needs custom
parsing beyond `res.json()`, when the call goes through the SSE
transport layer (use `transportAction`), or when the work involves
multiple sequential HTTP calls or non-HTTP side effects (use
`defineAction`).

### `transportAction` — for SSE-backed intents

For commands that flow through `transport.send` (prompt, resolve
pending change, restore checkpoint, fork chat). Internal to the
actions/ directory — external callers use `apiAction` or
`defineAction` instead.

```ts
import { transportAction } from "./transport.js";

export const restoreCheckpoint = transportAction<{ chatID: string; tag: string }>({
  name: "chat.restore_checkpoint",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, tag }) => ({
    type: "restore_checkpoint",
    chat_id: chatID,
    payload: { tag },
  }),
  retryable: "network",
  error: "Couldn't restore checkpoint",
});
```

**When NOT to use `transportAction`:** when the caller lives outside
`actions/` (use `defineAction` with a manual `transportSend` call),
or when the response needs parsing beyond ok/error (use
`defineAction`).

### `defineAction` — for everything else

When the work isn't a single HTTP or transport call (e.g. multi-step
flows, dynamic imports, clipboard writes, Promise.all fan-out):

```ts
import { defineAction, ActionError } from "./index.js";

export const copyClipboard = defineAction<string, void>({
  name: "ui.copy_clipboard",
  run: async (text) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch (e) {
      throw new ActionError("Clipboard unavailable", { cause: e });
    }
  },
  success: "Copied",
  error: "Couldn't copy",
});
```

**When NOT to use `defineAction`:** when the work is a single HTTP
call (use `apiAction`) or a single transport command (use
`transportAction`). Those factories handle signal wiring, error
normalisation, and status-code classification automatically.

## Optimistic + rollback contract

The lifecycle:

1. `optimistic(args)` runs synchronously. Mutate the local store /
   DOM to reflect the assumed-successful state. Return any state
   needed to undo it (the **OptimisticOp**).
2. `run(args, signal)` runs. Throws `ActionError` on failure.
3. On success — the optimistic mutation stays.
4. On failure — `rollback(args, op, err)` runs to undo. The error
   is then toasted.
5. On cancellation — `rollback` runs (with `err.code === "cancelled"`
   and `err.message === "cancelled"`) but no toast fires. Both the
   success-path race (signal aborted after run resolved) and the
   error-path (run rejected while signal aborted) pass the same
   synthetic `{ message: "cancelled", code: "cancelled" }` shape to
   rollback for consistency.

```ts
export const renameChat = apiAction<{ id: string; name: string }, void>({
  name: "chat.rename",
  request: ({ id, name }) => ({
    method: "POST",
    path: `/api/chats/${id}/rename`,
    body: { name },
  }),
  optimistic: ({ id, name }) => {
    const before = store.getChatName(id);
    store.setChatName(id, name);
    return { before };  // captured for rollback
  },
  rollback: ({ id }, op) => {
    if (op !== undefined && typeof op === "object" && op !== null
        && "before" in op && typeof op.before === "string") {
      store.setChatName(id, op.before);
    }
  },
  error: "Couldn't rename",
});
```

The `OptimisticOp` is typed as `unknown` to keep the framework
agnostic; cast it inside `rollback` to the shape `optimistic`
returned. (TypeScript guards as above are best practice but
for trivial shapes a cast is fine.)

For compile-time safety between `optimistic` and `rollback`, specify
the third type parameter `TOp` on the action definition:

```ts
export const renameChat = apiAction<{ id: string; name: string }, void, { before: string }>({
  // ...
  optimistic: ({ id, name }) => {
    const before = store.getChatName(id);
    store.setChatName(id, name);
    return { before };  // typed as { before: string }
  },
  rollback: ({ id }, op) => {
    // op is typed as { before: string } | undefined — no cast needed
    if (op !== undefined) store.setChatName(id, op.before);
  },
});
```

When `TOp` isn't specified (defaults to `unknown`), narrow with a
type guard or cast inside `rollback`:

```ts
rollback: ({ id }, op) => {
  const o = op as { before: string } | undefined;
  if (o !== undefined) store.setChatName(id, o.before);
},
```

## Toast wiring

| Definition field      | Default behavior                                        |
|-----------------------|--------------------------------------------------------|
| `success` not set     | NO success toast (UI updates are usually visible)      |
| `success: "Saved"`    | Calls `toast.success("Saved")` on success              |
| `success: (a, r) => …`| Function receives args + result                        |
| `error` not set       | Toasts `"<NameTail> failed: <serverMessage>"`          |
| `error: "Prefix"`     | Toasts `"Prefix: <serverMessage>"`                     |
| `error: (a, e) => …`  | Function receives args + ActionError                   |
| `error: false`        | Suppresses error toast (rare — banner/inline instead)  |

Per-dispatch overrides:

```ts
await action.dispatch(args, { silent: true });          // skip success toast
await action.dispatch(args, { successMessage: "Custom" }); // override success text
await action.dispatch(args, { errorPrefix: "Per-call" }); // override error prefix
```

**Best practice:** include the resource identifier in error messages
so the user knows WHAT failed, not just that something failed:

```ts
error: (args) => `Couldn't rename \u201c${args.original}\u201d`,
error: (args) => `Merge failed for PR #${String(args.pr_number)}`,
```

## Error codes reference

`ActionError` carries optional `status` (HTTP) and `code` (string)
fields. The framework uses these for retry eligibility and toast
formatting. Canonical codes:

| Code          | Source                          | Retry-eligible (`"network"`) |
|---------------|---------------------------------|------------------------------|
| `"timeout"`   | DOMException TimeoutError       | Yes                          |
| `"network"`   | fetch TypeError (offline/DNS)   | Yes                          |
| `"cancelled"` | DOMException AbortError / user  | No (not an error)            |
| `"conflict"`  | HTTP 409                        | No                           |
| `"not_found"` | HTTP 404                        | No                           |
| `"dedupe"`    | Synthetic: deduped dispatch settled as non-success | No     |
| *(none)*      | Generic Error without code      | Only if `retryable: "always"`|

Status 0 (no HTTP response) is also retry-eligible under `"network"`.

Additionally, actions may define domain-specific codes for internal
classification (e.g. `"draft_failed"`, `"run_plan_failed"`). These
are NOT retry-eligible under `retryable: "network"` — only
`"network"` and `"timeout"` (plus `status === 0`) qualify.

Throw with explicit codes for server-side classification:

```ts
throw new ActionError("Server rejected", { status: 409, code: "conflict" });
throw new ActionError("Timed out", { code: "timeout" });
```

The `toActionError()` normaliser (internal) maps DOMException names
automatically: `TimeoutError` → `"timeout"`, `AbortError` →
`"cancelled"`.

**Rule:** any `catch` block around a raw `fetch()` call in a
`defineAction` runner MUST set `code: "network"` on the thrown
`ActionError` for network failures. Without it, `retryable: "network"`
and auto-retry won't fire. Use `apiAction` or `transportAction` to
get this for free.

## Cancellation

`action.cancel()` aborts every in-flight instance for that action.
The `run()` function receives an `AbortSignal` it must honour.
HTTP and transport adapters wire the signal automatically; custom
`defineAction` runners must check `signal.aborted` themselves or
pass the signal down to `fetch` / `EventSource` / etc.

Cancellation behaves like an error for the rollback hook (the
optimistic mutation IS undone) but does NOT fire an error toast
(cancellation is the user's intent, not a failure).

## Retry button

Set `retryable: 'network'` to surface a Retry button on error toasts
for network/timeout failures (status 0, code `'timeout'`, code
`'network'`). Use `'always'` only for fully idempotent actions.
Default: no retry.

Idempotent reads (GET) should set `retryable: 'network'`. Mutations
that aren't idempotent (POST creating resources) should NOT set
retryable unless paired with `idempotencyKey: true` (which makes
the server treat retries as the original request).

```ts
export const listFiles = apiAction<void, FileEntry[]>({
  name: "files.list",
  request: () => ({ method: "GET", path: "/api/files" }),
  retryable: "network",   // safe — GET is idempotent
  error: "Couldn't list files",
});
```

## Auto-retry with backoff

For idempotent actions whose users would prefer transient failures to
recover silently, set `retry: { count, delay, factor? }` alongside
`retryable`:

```ts
export const fetchModels = apiAction<void, ModelInfo[]>({
  name: "models.list",
  retryable: "network",
  retry: { count: 2, delay: 300 },   // 300ms then 600ms before surfacing
  request: () => ({ method: "GET", path: "/api/models" }),
});
```

The retry chain re-runs `run()` only — `optimistic()` does NOT re-fire,
its mutation persists across retries. The toast (and its Retry button)
only appear once auto-retry is exhausted. Backoff is `delay × factor^n`
capped at 5s, with `factor` defaulting to 2 (exponential).

`action.cancel()` aborts the retry chain mid-backoff; queued retries
unwind cleanly.

## Mutation scopes (serialization)

When two dispatches target the same resource and shouldn't run in
parallel (e.g. two settings PATCHes that race their dedup tracker, two
git operations on the same repo), give the action a `scope`:

```ts
// Static scope: ALL dispatches of this action serialize through one queue.
export const patchSettings = apiAction<Patch, void>({
  name: "settings.patch",
  scope: "settings",
  request: (p) => ({ method: "PATCH", path: "/api/settings", body: p }),
});

// Function scope: one queue PER resource. Different repos run in parallel.
export const gitPull = apiAction<{ repo: string }, void>({
  name: "git.pull",
  scope: (args) => `git:${args.repo}`,
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
});
```

Two actions sharing the same `scope` string serialize against each
other, not just within one action — useful when add/remove/patch
actions on the same data model must follow each other strictly.

**When NOT to use scope:** don't add scope to actions that should
run in parallel (independent file uploads, unrelated API calls,
read-only fetches for different resources). Scope serializes — use
it only when ordering or mutual exclusion matters. For preventing
accidental concurrent dispatches with the same args, prefer `dedupe`
(which collapses to one shared run) over `scope` (which queues
sequentially).

## Per-dispatch callbacks

`DispatchOptions` accepts `onSuccess` / `onError` / `onSettled` for
callsite-specific reactions without bloating the action definition:

```ts
const result = await saveDraftAction.dispatch(draft, {
  onSuccess: () => editor.focus(),
  onError:   () => editor.markDirty(),
  onSettled: () => closeProgressDialog(),
});
```

Callbacks fire AFTER the action-level toast emission. `onSettled`
fires for success, error, AND cancellation (similar to TanStack Query).

## Idempotency keys

Set `idempotencyKey: true` on an action definition to have the
framework generate a per-dispatch key. The key is sent as the
`Idempotency-Key` HTTP header for `apiAction` and as a
`idempotency_key` field in the command payload for `transportAction`.
A retry of the same dispatch sends the same key, so a server that
recognizes the header can dedupe and treat the retry as the original
request.

```ts
export const createPR = apiAction<CreatePRArgs, { number: number }>({
  name: "git.create_pr",
  idempotencyKey: true,           // server dedupes on header
  retryable: "network",
  retry: { count: 1, delay: 500 },
  request: (a) => ({ method: "POST", path: "...", body: a }),
});
```

This closes the "timed out but server processed" hole that prevents
retry on otherwise non-idempotent operations: even a POST that creates
a resource becomes safe to retry, since the server returns the
original response on the second request.

For custom defineAction implementations, the key is exposed via the
3rd context argument to `run`. The function is called once per
dispatch and the value is frozen across retries — meaning the key
identifies the LOGICAL request (not each retry attempt), giving the
server a stable dedup key:

```ts
const action = defineAction<MyArgs, MyResult>({
  name: "custom.action",
  // Called once per dispatch; result is frozen across retries.
  // Use a deterministic-per-dispatch nonce.
  idempotencyKey: (args) => `${args.userId}:${crypto.randomUUID()}`,
  run: async (args, signal, ctx) => {
    return await fetch("/x", {
      method: "POST",
      headers: ctx?.idempotencyKey !== undefined
        ? { "Idempotency-Key": ctx.idempotencyKey }
        : {},
      body: JSON.stringify(args),
      signal,
    }).then((r) => r.json());
  },
});
```

**Interaction with `dedupe`:** `idempotencyKey` is server-side
deduplication (across retries of one dispatch). `dedupe` is
client-side (collapses concurrent dispatches with matching args
into one in-flight call). They are complementary: use both when
both retries AND accidental double-clicks are concerns. They aren't
redundant — `dedupe` never reaches the server; `idempotencyKey` only
helps when the request actually fires.

## Request deduplication

Set `dedupe: true` on an action definition to collapse concurrent
dispatches with matching args into a single in-flight promise. The
second caller gets the SAME promise back, no new optimistic fires,
no duplicate run() call. Per-call `onSuccess` / `onError` callbacks
on the deduped caller fire with the original dispatch's actual
outcome (real error, not a synthetic stub).

```ts
export const fetchSidebarBadges = apiAction<void, BadgeData>({
  name: "ui.fetch_badges",
  dedupe: true,                    // accidental double-call collapses
  request: () => ({ method: "GET", path: "/api/badges" }),
});
```

Different from `scope` (which queues sequentially): dedupe collapses
to one. Use dedupe for accidental double-clicks not yet covered by
`bindLoadingState`, or for reads triggered from two unrelated effects
that should land in the same network call.

## Debounced dispatch

`debouncedDispatch(action, { wait, leading? })` wraps an action so
rapid calls coalesce into a single dispatch after a quiet window.

```ts
import { debouncedDispatch } from "./actions/index.js";

const search = debouncedDispatch(searchAction, { wait: 300 });

input.addEventListener("input", (e) => {
  const target = e.target as HTMLInputElement;
  search({ query: target.value });     // last-typed value wins
});
input.addEventListener("blur", () => {
  search.cancel();                     // drop pending if blurred
});
form.addEventListener("submit", () => {
  search.flush();                      // fire immediately (e.g. Enter)
});
```

Replaces ad-hoc `setTimeout` + `clearTimeout` chains for typeahead
search, slash-command option fetches, auto-save. Set
`leading: true` for leading+trailing semantics: the first call fires
immediately, subsequent calls within the wait window are suppressed
but the most-recent ones fire automatically when the window expires.
`flush()` and `cancel()` work in both modes.

**`cancel()` and leading-mode cooldown:** `cancel()` clears the
pending timer and drops queued args, but does NOT reset the internal
`lastFiredAt` timestamp. This means the next call within `wait` ms
after the last leading-edge fire is still suppressed — cancelling
does not re-open the cooldown window.

## Pending count + multi-action loading state

For surfaces that need to know about multiple actions:

```ts
import { pendingCount, pendingForAny, subscribeToActions } from "./actions/index.js";

// Global app-bar progress indicator:
const progressBar = $.appProgress;
subscribeToActions(() => {
  progressBar.classList.toggle("active", pendingCount() > 0);
});

// One Save button covers multiple settings actions:
subscribeToActions(() => {
  const busy = pendingForAny([
    "settings.patch",
    "settings.save_steering",
    "settings.set_kiro_setting",
  ]);
  saveBtn.disabled = busy;
});
```

## Action status snapshots (internal)

> **Note:** `actionStatus` is `@internal` — not exported from the
> public surface (`actions/index.ts`). Import directly from
> `./actions/action-status.js` if needed within the framework.

For UIs that need richer state than just "is it pending":

```ts
// Internal use only — not part of the public API.
import { actionStatus } from "./action-status.js";

const status = actionStatus("settings.save_steering");
// status is a live-updating object — same reference, mutated in place.
// { pending: number, lastError?, lastSuccess?, lastDispatchedAt, lastSettledAt }
```

## Naming convention

Use `<area>.<verb>` or `<area>.<verb_noun>` with lowercase + underscores:

```
chat.archive              chat.cancel_turn          chat.clear_pending_trust
chat.delete               chat.delete_archived      chat.discard_tangent
chat.fork                 chat.load_history         chat.merge_tangent
chat.resolve_all_pending  chat.resolve_pending_change  chat.respond_permission
chat.restore              chat.restore_checkpoint   chat.send_prompt
chat.set_auto_approve_crew  chat.set_supervised    chat.switch_model
chat.trust_pending
checkpoint.preview
conflicts.load            conflicts.open_diff
crew.send_message
editor.fetch_agent_lines  editor.load_diff          editor.resolve_partial
editor.save_file          editor.send_plan          editor.suggest_resolution
files.create_file         files.create_folder       files.delete
files.download            files.rename              files.upload
forge.clone_repo          forge.connect_pat         forge.delete_local
forge.sign_out            forge.start_device_flow
git.checkout_branch       git.close_pr              git.commit
git.discard               git.generate_message      git.merge_pr
git.pull                  git.push                  git.refresh_prs
git.stage                 git.stash                 git.stash_pop
git.unstage
mcp.delete_server         mcp.open_edit             mcp.save_server
mcp.search_registry       mcp.toggle_server
messages.explain_error    messages.undo_edit
notify.register_push
permissions.add_rule      permissions.remove_rule
plan.run
settings.load_kiro_config settings.logout           settings.patch
settings.refresh_retention  settings.save_steering  settings.set_kiro_setting
tools.install             tools.load_list           tools.run_diagnostics
tools.save                tools.seed_mcp
ui.copy_clipboard
```

(76 actions as of 2026-05-25.)

This is the registry key for log queries, telemetry, and tests.
Pick once and don't change — callers may grep for it.

## Where to put action definitions

- One file per area: `actions/files.ts`, `actions/chat.ts`,
  `actions/settings.ts`, etc.
- Definitions are module-level constants. They're cheap (just a
  closure) and registering many is fine.
- Import the action where it's dispatched:

  ```ts
  // ui-file.ts
  import { deleteFile } from "./actions/files.js";

  deleteBtn.addEventListener("click", () => {
    void deleteFile.dispatch(targetPath);
  });
  ```

## Decision: store.ts loadList / loadMessages remain outside actions

**Re-evaluated:** Cycle 4 (2026-05-25), after dedupe key fix
(JSON.stringify via safeStringify) and abort-mid-fetch fix
(signal.aborted check in runOnce).

**Decision:** Do NOT migrate. Cancel-replace remains the correct
semantic.

**Rationale:**

1. **Cancel-replace ≠ dedupe or scope.** Both loaders intentionally
   abort the previous in-flight request and start fresh. The action
   framework offers dedupe (collapse to one shared promise) and scope
   (queue sequentially). Neither models "discard stale, start new."
   A stale loadList from 5s ago during a gap should be aborted, not
   shared.

2. **Pagination args break dedupe.** `loadMessages(chatID, before)`
   is called with different `before` timestamps. Different args =
   different dedupe keys = no collapse. Scope would queue them, but
   the desired behavior on chat-switch is abort-old, not queue-behind.

3. **Per-chatID controller map.** `loadMessages` uses a per-chatID
   abort map — the action framework has no "cancel-replace per
   resource" primitive. `scope` serializes; `dedupe` collapses.
   Neither aborts the prior call.

4. **No toast, no optimistic, no retry button.** These are background
   data-loading operations. Callers render their own retry UI
   (chat.ts's retry button). The action framework's value-add doesn't
   apply.

5. **registerCleanup already handles unload.** The store already uses
   `registerCleanup()` for page-unload abort — same mechanism the
   action framework uses internally.

6. **Return-value contract.** Callers use `.then((ok) => { ... })` to
   conditionally render skeletons, retry buttons, scroll. Refactoring
   all callsites for `dispatch()` → `Promise<T | null>` adds churn
   with no behavioral gain.

**What changed since cycle 2 that was re-evaluated:**
- `safeStringify` in dedupe key: makes dedupe keys correct for complex
  args, but doesn't change the semantic mismatch (cancel-replace ≠
  dedupe).
- `signal.aborted` check after run resolves: makes the framework
  strictly safer, but the store already has its own equivalent guard.

**Conclusion:** The "Don't use an action for background polls / read-
only fetches that auto-recover" guidance in the "When to use" section
above continues to apply. These loaders are cancel-replace reads, not
user-initiated mutations.

## Best practices

### 1. Scope key format: `<area>:<resource-id>`

Use a consistent prefix so different actions on the same resource
serialize against each other:

```ts
scope: (args) => `chat:${args.chatID}`   // chat mutations
scope: (args) => `git:${args.repo}`      // git operations per repo
scope: (args) => `mcp:${args.id}`        // MCP server mutations
scope: "settings"                        // static: one global queue
```

### 2. Prefer `TOp` type parameter over manual casts

When both `optimistic` and `rollback` are defined, specify the third
type parameter for compile-time safety:

```ts
apiAction<Args, Result, { before: string }>({ ... })
```

Reserve `as` casts for cases where the op shape is complex or
shared across multiple actions.

### 3. Set `retryable` + `retry` together for idempotent reads

`retryable` controls the Retry button; `retry` controls silent
auto-retry. For GET-based actions, always set both:

```ts
retryable: "network",
retry: { count: 2, delay: 300 },
```

Never set `retry` on non-idempotent mutations without
`idempotencyKey`.

### 4. Use `error: false` only with a custom error surface

When suppressing the error toast, the action MUST have an
alternative error surface (send-state button, inline banner,
caller-rendered retry UI). Document which surface handles it:

```ts
error: false,  // send-state.ts blocked-button is the surface
```

### 5. Scope key for per-resource vs static

Use a function scope when different resources should run in
parallel. Use a static string when ALL dispatches must serialize:

```ts
// Per-resource: different repos run in parallel
scope: (args) => `git:${args.repo}`

// Static: all settings patches serialize globally
scope: "settings"
```

### 6. `idempotencyKey` for non-idempotent mutations with retry

When a POST creates a resource but you want retry safety, combine
`idempotencyKey: true` with `retryable: "network"`. The server
dedupes on the header so a timed-out-but-processed request won't
create duplicates on retry.

### 7. Always `void` the dispatch at fire-and-forget callsites

```ts
void deleteFile.dispatch(path);  // no floating promise lint error
```

Only `await` when the caller needs the result or must sequence
after completion.

## Testing actions

Mock the toast module + assert on the registry log:

```ts
import { vi } from "vitest";
vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));
import { recentLog, _resetForTest } from "./registry.js";
import { deleteFile } from "./files.js";

beforeEach(() => _resetForTest());

test("deleteFile records success", async () => {
  // Mock fetch to return 200 OK
  vi.stubGlobal("fetch", () => Promise.resolve(new Response("{}", { status: 200 })));
  await deleteFile.dispatch("/some/path");
  expect(recentLog()[0]?.status).toBe("success");
});
```

For optimistic/rollback semantics, assert on your local store's
state at the right point in the lifecycle.
