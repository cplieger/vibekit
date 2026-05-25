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

export const createFile = apiAction<{ dir: string; name: string }, unknown>({
  name: "files.create_file",
  scope: (args) => "dir:" + args.dir,
  retry: { count: 2, delay: 300 },
  retryable: "network",
  request: (args) => ({
    method: "POST",
    path: "/api/files/action",
    body: { action: "touch", path: args.dir + "/" + args.name },
  }),
  error: "Couldn't create file",
});

// Dispatch site:
const ok = await createFile.dispatch({ dir: "/src", name: "util.ts" });
if (ok === null) return;  // toast already fired
```

**When NOT to use `apiAction`:** when the response needs custom
parsing beyond `res.json()`, when the call goes through the SSE
transport layer (use `transportAction`), when the work involves
multiple sequential HTTP calls or non-HTTP side effects (use
`defineAction`), or when a batch of parallel fetches must aggregate
errors (use `defineAction` with `Promise.all`).

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

Transient HTTP statuses (retry-eligible under `"network"` mode):

| Status | Meaning              | Retry-eligible |
|--------|----------------------|----------------|
| 408    | Request Timeout      | Yes            |
| 429    | Too Many Requests    | Yes            |
| 502    | Bad Gateway          | Yes            |
| 503    | Service Unavailable  | Yes            |
| 504    | Gateway Timeout      | Yes            |

Non-transient statuses (e.g. 400, 401, 403, 404, 409) are NOT
retry-eligible under `"network"` — they indicate permanent client
or server-side rejections that re-dispatching won't fix.

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

### `action.isInflight`

A convenience boolean getter on the action object. Returns `true`
when at least one dispatch is currently in-flight (pending), `false`
otherwise. Reflects cancellation immediately — calling `cancel()`
sets it to `false` without waiting for the scope chain to advance.

```ts
const save = defineAction({ name: "doc.save", run: ... });
saveBtn.disabled = save.isInflight;  // disable while saving
```

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

### `retryArgs` — fresh args at retry-click time

When the Retry button is clicked, the framework normally re-dispatches
with a `structuredClone` of the original args. This is stale when args
contain DOM references, live getters, or mutable arrays that change
between the original dispatch and the retry click.

Set `retryArgs` to compute fresh args at click time:

```ts
export const deleteSelected = apiAction<{ listEl: HTMLElement; names: string[] }, void>({
  name: "files.delete_selected",
  retryable: "network",
  retryArgs: (original) => {
    // Re-read the current selection from the DOM at retry time
    const listEl = document.querySelector<HTMLElement>("#file-list");
    if (!listEl) return null;  // suppress retry if element gone
    const names = [...listEl.querySelectorAll(".selected")].map(el => el.textContent!);
    return { listEl, names };
  },
  request: (args) => ({ method: "POST", path: "/api/files/delete", body: { names: args.names } }),
  error: "Couldn't delete files",
});
```

- Return `null` from `retryArgs` to suppress the retry (click becomes a no-op).
- If `retryArgs` throws, the retry is silently suppressed (no crash).
- The `original` parameter is a best-effort clone of the original args
  (for extracting stable identifiers like IDs or paths).

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

`DispatchOptions` accepts `onSuccess` / `onError` / `onCancel` /
`onSettled` / `onRetryAttempt` / `onRetryExhausted` for callsite-specific
reactions without bloating the action definition:

```ts
const result = await saveDraftAction.dispatch(draft, {
  onSuccess: (result, args) => editor.focus(),
  onError:   (err, args) => editor.markDirty(),
  onCancel:  (args) => editor.clearProgress(),
  onSettled: (args) => closeProgressDialog(),
  onRetryAttempt: (info, args) => console.log(`Retry ${info.attempt}/${info.maxAttempts}`),
});
```

Signatures:
- `onSuccess(result: TResult, args: TArgs)` — fires only on success
- `onError(err: ActionErrorLike, args: TArgs)` — fires only on error (NOT cancellation)
- `onCancel(args: TArgs)` — fires only on cancellation (NOT error). Use to distinguish user-initiated abort from failure without inspecting `onError`'s absence.
- `onSettled(args: TArgs)` — fires for success, error, AND cancellation
- `onRetryAttempt(info: RetryAttemptInfo, args: TArgs)` — fires before each retry attempt (not the initial attempt). `info` contains `{ attempt, maxAttempts, error, delay }`.
- `onRetryExhausted(info: { error, attempts }, args: TArgs)` — fires when all auto-retries have failed, before the error toast. Useful for telemetry that distinguishes retry exhaustion from first-attempt failures.

Callbacks fire AFTER the action-level toast emission. `onSettled`
fires in the `finally` block, so it runs even if `onSuccess` or
`onError` throws. Throwing inside any callback is caught and logged
— it never disrupts the dispatch promise or other callbacks.

**Callback ordering guarantee** (when retry is configured):

1. `onRetryAttempt` — once per retry attempt (not the initial)
2. On exhaustion: `onRetryExhausted` → `onError` → `onSettled`
3. On success after retry: `onSuccess` → `onSettled`
4. On cancel during retry/backoff: `onCancel` → `onSettled`

For scoped actions, all callbacks for dispatch N complete before
dispatch N+1's `run()` begins. This means `onSettled` of the first
dispatch fires before the second dispatch starts its work.

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

## Action status snapshots

For UIs that need richer state than just "is it pending":

```ts
import { actionStatus } from "./actions/index.js";

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
git-badge.forges          git-badge.refresh         git-badge.status
git.checkout_branch       git.close_pr              git.commit
git.discard               git.generate_message      git.merge_pr
git.pull                  git.push                  git.refresh_prs
git.stage                 git.stash                 git.stash_pop
git.unstage
mcp.delete_server         mcp.open_edit             mcp.save_server
mcp.search_registry       mcp.toggle_server
messages.explain_error    messages.undo_edit
notify.register_push      notify.unsubscribe_push
permissions.add_rule      permissions.remove_rule
plan.run
settings.load             settings.load_kiro_config settings.logout
settings.patch            settings.refresh_retention  settings.save_steering
settings.set_kiro_setting
tools.install             tools.load_list           tools.run_diagnostics
tools.save                tools.seed_mcp
ui.copy_clipboard
```

(81 actions as of 2026-05-25.)

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
  import { deleteFilesBatch } from "./actions/files.js";

  deleteBtn.addEventListener("click", () => {
    void deleteFilesBatch.dispatch({ dir: currentDir, names: selected, listEl });
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
void deleteFilesBatch.dispatch({ dir, names, listEl });  // no floating promise lint error
```

Only `await` when the caller needs the result or must sequence
after completion.

### 8. Avoid `optimistic` on `dedupe: true` actions

When a dispatch dedupes (collapses into an existing in-flight call),
`optimistic()` does NOT fire for the deduped caller. If the action
relies on optimistic UI, the second caller's UI won't update until
the shared promise resolves. Use `dedupe` only for actions where
the result alone drives the UI (reads, badge refreshes).

### 9. `onSettled` fires even on cancellation

`onSettled` fires for success, error, AND cancellation. Use it for
cleanup that must run regardless of outcome (closing dialogs,
releasing locks). Don't use it for success-only reactions — use
`onSuccess` instead.

### 10. Scope + retry: retries run WITHIN the scope slot

When an action has both `scope` and `retry`, the retry loop executes
inside the scope's serial slot. The scope chain does NOT advance to
the next queued dispatch until all retries exhaust (or succeed). This
means a scoped action with `retry: { count: 3, delay: 500 }` can
hold its scope slot for up to ~3.5s on transient failures. Keep retry
budgets short on scoped actions to avoid starving queued dispatches:

```ts
// Good: short retry budget on a scoped action
scope: "settings",
retry: { count: 2, delay: 200 },   // max ~600ms hold

// Bad: long retry budget blocks the scope queue
scope: "settings",
retry: { count: 5, delay: 1000 },  // up to ~31s hold
```

### 11. Callbacks fire after scope slot releases

`onSettled` runs in the `finally` block of `runOnce`, which executes
after the scope chain's `.then()` resolves. This means dispatching
the same scoped action from within `onSettled` does NOT deadlock —
the scope slot is already released. However, dispatching from
`onSuccess` or `onError` (which fire before `finally`) also works
because the scope chain tracks the outer promise, not the callback
execution.

### 12. Permanent failure codes bypass retry

The following codes are NEVER retried, even with `retryable: "always"`:
`"cancelled"`, `"send_failed"`, `"clipboard"`, `"unsupported"`,
`"server_rejected"`. These represent permanent failures where
retrying would not help. If your custom `defineAction` runner throws
with one of these codes, auto-retry and the Retry button are both
suppressed.

### 13. Use `classifyFetchError` in custom `defineAction` runners

When writing a `defineAction` that calls `fetch` directly, use
`classifyFetchError` from the error module to normalise catch-block
errors into retry-eligible `ActionError` instances:

```ts
import { defineAction, ActionError, classifyFetchError } from "./index.js";

const myAction = defineAction<Args, Result>({
  name: "custom.fetch",
  retryable: "network",
  retry: { count: 2, delay: 300 },
  run: async (args, signal) => {
    let res: Response;
    try {
      res = await fetch("/api/thing", { method: "POST", body: JSON.stringify(args), signal });
    } catch (e) {
      throw classifyFetchError(e, signal);  // sets code: "network" | "timeout" | "cancelled"
    }
    if (!res.ok) throw new ActionError("Server error", { status: res.status });
    return res.json() as Promise<Result>;
  },
});
```

Without `classifyFetchError`, a raw `TypeError` from `fetch` won't
carry `code: "network"` and `retryable: "network"` won't trigger.
`apiAction` and `transportAction` handle this automatically.

## Testing actions

Mock the toast module + assert on the registry log:

```ts
import { vi } from "vitest";
vi.mock("../toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));
import { recentLog, _resetForTest } from "./registry.js";
import { createFile } from "./files.js";

beforeEach(() => _resetForTest());

test("createFile records success", async () => {
  // Mock fetch to return 200 OK
  vi.stubGlobal("fetch", () => Promise.resolve(new Response("{}", { status: 200 })));
  await createFile.dispatch({ dir: "/src", name: "util.ts" });
  expect(recentLog()[0]?.status).toBe("success");
});
```

For optimistic/rollback semantics, assert on your local store's
state at the right point in the lifecycle.
