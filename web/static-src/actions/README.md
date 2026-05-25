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

### `transportAction` — for SSE-backed intents

For commands that flow through `transport.send` (prompt, resolve
pending change, restore checkpoint, fork chat):

```ts
import { transportAction } from "./index.js";

export const sendPrompt = transportAction<{ chatID: string; text: string }>({
  name: "chat.send_prompt",
  command: ({ chatID, text }) => ({
    type: "prompt",
    chat_id: chatID,
    payload: { text },
  }),
  // Agent's response stream IS the success feedback — no toast needed.
  error: "Couldn't send",
});
```

### `defineAction` — for everything else

When the work isn't a single HTTP or transport call (e.g. multi-step
flows, dynamic imports, clipboard writes, Promise.all fan-out):

```ts
import { defineAction, ActionError } from "./index.js";

export const copyToClipboard = defineAction<string, void>({
  name: "ui.clipboard_copy",
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
retryable.

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
3rd context argument to `run`:

```ts
const action = defineAction<MyArgs, MyResult>({
  name: "custom.action",
  idempotencyKey: (args) => `${args.userId}:${Date.now()}`,
  run: async (args, signal, ctx) => {
    return await fetch("/x", {
      method: "POST",
      headers: { "Idempotency-Key": ctx?.idempotencyKey ?? "" },
      body: JSON.stringify(args),
      signal,
    }).then((r) => r.json());
  },
});
```

## Request deduplication

Set `dedupe: true` on an action definition to collapse concurrent
dispatches with matching args into a single in-flight promise. The
second caller gets the SAME promise back, no new optimistic fires,
no duplicate run() call.

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
  search({ query: e.target.value });   // last-typed value wins
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
`leading: true` for leading-edge mode (fire immediately, suppress
trailing fires within the window).

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

renderTimestamp(status.lastSettledAt);
if (status.lastError !== undefined) {
  banner.show(status.lastError.message);
}
```

## Naming convention

Use `<area>.<verb>` with lowercase + underscores or hyphens:

```
chat.delete         chat.archive       chat.fork
files.delete        files.create       files.rename
settings.save       settings.toggle    settings.steering_save
mcp.add_server      mcp.delete_server  mcp.toggle_server
git.commit          git.push           git.stage_all
```

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
