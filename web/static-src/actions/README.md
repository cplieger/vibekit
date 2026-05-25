# actions/

A declarative framework for user-initiated mutations. Wraps the
imperative raw-fetch / `transport.send` pattern with a single
contract: every action declares its name, request, optional
optimistic UI, optional rollback, retry/scope/dedupe behavior, and
toast wiring up front. The dispatcher handles the lifecycle.

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

## The two factories

### `apiAction` — the 90% case

For any mutation that's "POST/PUT/DELETE/PATCH this URL, surface
the server error":

```ts
import { apiAction } from "./actions/index.js";

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

### `defineAction` — for everything else

When the work isn't a single HTTP call (multi-step flows, dynamic
imports, clipboard writes, Promise.all fan-out, custom parsing), or
when you need the SSE-backed `transport.send` path:

```ts
import { defineAction, ActionError } from "./actions/index.js";

export const copyToClipboard = defineAction<string, void>({
  name: "ui.clipboard_copy",
  run: async (text) => {
    try { await navigator.clipboard.writeText(text); }
    catch (e) { throw new ActionError("Clipboard unavailable", { cause: e }); }
  },
  success: "Copied",
  error: "Couldn't copy",
});
```

Note: for SSE-backed intents (prompt, resolve pending change,
fork chat), use `transportAction` from `actions/transport.js`. It's
not exported from `index.ts` because only action modules inside
`actions/` import it (chat.ts, editor.ts, messages.ts, crew.ts).

## Optimistic + rollback

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
    return { before };  // captured as TOp for rollback
  },
  rollback: ({ id }, op) => {
    if (op !== undefined) store.setChatName(id, op.before);
  },
  error: "Couldn't rename",
});
```

Lifecycle:
1. `optimistic(args)` runs synchronously. Mutate the local store /
   DOM and return the undo state.
2. `run(args, signal)` runs (with auto-retry if configured).
3. Success → optimistic stays; success toast (if any) fires.
4. Error → `rollback(args, op, err)` runs to undo, then error toast.
5. Cancellation → `rollback` runs (with `err.code === "cancelled"`)
   but no toast fires.

The 3rd type parameter to `apiAction`/`defineAction` (TOp) propagates
the optimistic return type into rollback's `op` parameter — no manual
casts needed for typed undo state.

## Retry

Auto-retry transient failures BEFORE surfacing the error toast:

```ts
import { RETRY_STANDARD } from "./actions/index.js";

export const stageFile = apiAction<string, void>({
  name: "git.stage",
  request: (path) => ({ method: "POST", path: "/api/git/stage", body: { path } }),
  retryable: "network",     // only retry network/timeout/transient HTTP
  retry: RETRY_STANDARD,    // 2 retries, 300ms initial delay, exponential backoff
  error: "Couldn't stage",
});
```

Custom config: `retry: { count: 3, delay: 200, factor: 2 }`. Backoff
is `delay × factor^attempt`, capped at 5s. Idempotency keys (if set)
are stable across retries so the server can dedupe.

`retryable: "network"` retries: codes `"network"`/`"timeout"`,
`status: 0`, and HTTP `408/429/502/503/504`. Permanent codes
(`cancelled`, `send_failed`, `clipboard`, `unsupported`,
`server_rejected`) are never retried.

`retryable: "always"` retries any error except permanent codes — use
only for fully idempotent operations.

When auto-retry exhausts, the error toast also gets a manual Retry
button (re-dispatches with the original args).

## Scope (serialization)

Serialize related dispatches through a per-key FIFO queue:

```ts
export const stageFile = apiAction<string, void>({
  name: "git.stage",
  scope: "git",  // all git mutations serialize against each other
  // ...
});
```

`scope` can be a static string (one queue for the whole action) or a
function of args (per-resource queue, e.g. `scope: (args) => "repo:" + args.repo`).
Two different actions sharing the same scope key serialize together.

Without `scope`, dispatches run in parallel.

## Dedupe (collision suppression)

Collapse concurrent dispatches with matching key into one in-flight
promise. The second dispatch returns the same promise as the first.

```ts
export const fetchOptions = apiAction<string, Option[]>({
  name: "commands.fetchOptions",
  dedupe: true,  // dedupe on JSON.stringify(args)
  request: (q) => ({ method: "GET", path: "/api/commands?q=" + q }),
  retryable: "network",
});
```

`dedupe` accepts `true` (key from `JSON.stringify(args)`) or a function
returning a string. Different from `scope` — scope queues sequentially;
dedupe collapses to one shared in-flight result.

## Idempotency keys

Generate a per-dispatch key the server can use to dedupe retries:

```ts
export const cloneRepo = apiAction<{ url: string }, void>({
  name: "git.clone_repo",
  idempotencyKey: true,   // framework generates a ULID-like key
  retry: RETRY_STANDARD,
  retryable: "network",
  request: ({ url }) => ({
    method: "POST",
    path: "/api/git/clone",
    body: { url },
  }),
});
```

`apiAction` sends it as the `Idempotency-Key` HTTP header; `transportAction`
includes it in the command payload as `idempotency_key`. The key is
generated ONCE per dispatch (not per retry) so retries reuse it.

For custom keys: `idempotencyKey: (args) => "clone:" + args.url`.

## Per-dispatch options

```ts
await action.dispatch(args, {
  silent: true,                    // suppress success toast
  errorPrefix: "Per-call prefix",  // override error toast prefix
  onSuccess: (result, args) => {…},
  onError: (err, args) => {…},
  onSettled: (args) => {…},        // success OR error OR cancellation
});
```

## Cancellation

`action.cancel()` aborts every in-flight instance for that action.
The `run()` function receives an `AbortSignal`. HTTP and transport
adapters wire it automatically; custom `defineAction` runners must
honour it themselves.

Cancellation behaves like an error for the rollback hook (optimistic
mutation IS undone) but does NOT fire an error toast.

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

## Loading-state binding

Bind a button to one or more action names — disabled while ANY of
them is pending:

```ts
import { bindLoadingState } from "./actions/index.js";

// Single action:
const unbind = bindLoadingState("git.commit", commitBtn);

// Multiple actions (OR semantics):
const unbind = bindLoadingState(
  ["git.push", "git.pull", "git.stash"],
  pushBtn,
);

// On teardown:
unbind();
```

Options: `{ ariaBusy, preserveDisabled, disabledFn, pendingClass }`.
See `loading.ts` for details.

## Debouncing

Wrap an action so rapid calls coalesce into a single dispatch:

```ts
import { debouncedDispatch } from "./actions/index.js";

const debouncedSearch = debouncedDispatch(searchAction, { wait: 300 });
inputEl.addEventListener("input", () => debouncedSearch(inputEl.value));
inputEl.addEventListener("keydown", (e) => {
  if (e.key === "Enter") debouncedSearch.flush();
});
```

`wait`: quiet window in ms. `leading: true` fires on the leading edge
instead of the trailing edge. Returns `{ flush, cancel, isPending }`.

## Registry observers

```ts
import { subscribeToActions, pendingCount } from "./actions/index.js";

// Per-event listener (all actions):
const unsub = subscribeToActions((inst) => {
  if (inst.status === "error") capture(inst.error);
});

// Global counter (drives app-bar progress):
if (pendingCount() > 0) showSpinner();

// Per-action-set count (drives "all settings settled" detection):
if (pendingCount(["settings.patch", "settings.save_steering"]) === 0) finalize();
```

## Cleanup hooks

```ts
import { registerCleanup } from "./actions/index.js";

const unregister = registerCleanup(() => {
  controller.abort();
  clearTimeout(pollTimer);
});
// On module re-init: unregister() to detach the stale hook.
```

The framework auto-installs a `beforeunload` listener that drains
all registered actions + cleanup hooks.

## Naming convention

Use `<area>.<verb>` with lowercase + underscores:

```
chat.delete           chat.fork           chat.send_prompt
files.delete          files.create        files.rename
settings.patch        settings.save_steering
mcp.add_server        mcp.delete_server   mcp.toggle_server
git.commit            git.push            git.stage
```

This is the registry key — pick once and don't change.

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
  vi.stubGlobal("fetch", () => Promise.resolve(new Response("{}", { status: 200 })));
  await deleteFile.dispatch("/some/path");
  expect(recentLog()[0]?.status).toBe("success");
});
```
