# actions/

A declarative framework for user-initiated mutations. Wraps the
imperative `apiPost` / `transport.send` pattern with a single
contract: every action declares its name, request, optional
optimistic UI, optional rollback, and toast wiring up front. The
dispatcher handles the lifecycle.

## When to use

- **Use an action** for any mutation triggered by the user
  (button click, form submit, keystroke). Saves, deletes, sends,
  uploads, toggles.
- **Don't use an action** for background polls (use plain
  `apiGet` / `apiGetTyped`), SSE bus handlers, or read-only fetches
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
