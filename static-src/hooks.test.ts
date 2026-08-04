// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for hooks.ts: list render (runCommand vs askAgent rows, badges,
// enabled toggle, disabled state), empty/error states, the enable/disable
// toggle → dispatch + refetch, the "Run now" flow → output panel, open-file,
// and the debounced SSE refetch. api-client, the hook actions, toast, bus, and
// the editor opener are mocked so we control the fetched payload + dispatch
// results and assert the rendered DOM.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

vi.mock("./toast.js", () => ({ showToast: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_PLAY: "<svg data-play></svg>",
  ICON_EDIT_14: "<svg data-edit></svg>",
}));
vi.mock("./bus.js", () => ({ onSSE: vi.fn(() => () => undefined) }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./actions/hooks.js", () => ({
  setHookEnabled: { dispatch: vi.fn() },
  runHook: { dispatch: vi.fn() },
}));
vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(),
  CancellableSlot: class {
    start(): AbortSignal {
      return new AbortController().signal;
    }
    abort(): void {
      /* noop */
    }
  },
}));
vi.mock("./dom.js", () => ({ byId: (id: string) => document.getElementById(id) }));

import { apiGetTyped } from "./api-client.js";
import { split, join } from "@cplieger/keyenc";
import { onSSE } from "./bus.js";
import { openFile } from "./editor-openers.js";
import { showToast } from "./toast.js";
import { runHook, setHookEnabled } from "./actions/hooks.js";
import { _resetForTest, initHooks, loadHooks } from "./hooks.js";

const mockGet = vi.mocked(apiGetTyped);
const mockSetEnabled = vi.mocked(setHookEnabled.dispatch);
const mockRun = vi.mocked(runHook.dispatch);

interface HookLike {
  id: string;
  name: string;
  trigger: string;
  action_type: string;
  scope?: string;
  command?: string;
  prompt?: string;
  matcher?: string;
  file_path?: string;
  disabled_reason?: string;
  enabled: boolean;
}

function cmdHook(over: Partial<HookLike> = {}): HookLike {
  return {
    id: "id-greet",
    name: "greet",
    trigger: "Manual",
    action_type: "runCommand",
    command: "echo hello",
    file_path: ".kiro/hooks/greet.json",
    enabled: true,
    ...over,
  };
}

function agentHook(over: Partial<HookLike> = {}): HookLike {
  return {
    id: "id-ask",
    name: "askit",
    trigger: "Manual",
    action_type: "askAgent",
    prompt: "Say OK",
    enabled: true,
    ...over,
  };
}

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

function seedDom(): void {
  document.body.innerHTML = `
    <div id="hooks-section">
      <div id="hooks-list"><div class="list-empty">No hooks yet.</div></div>
    </div>`;
}

const list = (): HTMLElement => document.getElementById("hooks-list") as HTMLElement;

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  _resetForTest();
  seedDom();
  mockGet.mockResolvedValue({ hooks: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("initHooks", () => {
  it("subscribes to the hooks_changed SSE", () => {
    initHooks();
    expect(vi.mocked(onSSE)).toHaveBeenCalledWith("hooks_changed", expect.any(Function));
  });
});

describe("loadHooks render", () => {
  it("renders a runCommand row with command, Command badge, run button, checked toggle", async () => {
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();
    const row = list().querySelector(".hook-row") as HTMLElement;
    expect(row.textContent).toContain("greet");
    expect(row.textContent).toContain("Manual");
    expect(row.textContent).toContain("Command");
    expect(row.textContent).toContain("echo hello");
    expect(row.querySelector(".hook-run")).not.toBeNull();
    expect((row.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
  });

  it("renders an askAgent row with the Agent badge, the prompt, and no run button", async () => {
    mockGet.mockResolvedValue({ hooks: [agentHook()] });
    loadHooks();
    await flush();
    const row = list().querySelector(".hook-row") as HTMLElement;
    expect(row.textContent).toContain("Agent");
    expect(row.textContent).toContain("Say OK");
    expect(row.querySelector(".hook-run")).toBeNull();
  });

  it("marks a disabled hook (unchecked toggle + hook-off class)", async () => {
    mockGet.mockResolvedValue({ hooks: [cmdHook({ enabled: false })] });
    loadHooks();
    await flush();
    const row = list().querySelector(".hook-row") as HTMLElement;
    expect(row.classList.contains("hook-off")).toBe(true);
    expect((row.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);
  });

  it("renders a global hook (kiro-cli 2.13) with the Global badge and no open button", async () => {
    mockGet.mockResolvedValue({
      hooks: [cmdHook({ scope: "global", file_path: "~/.kiro/hooks/greet.json" })],
    });
    loadHooks();
    await flush();
    const row = list().querySelector(".hook-row") as HTMLElement;
    const badge = row.querySelector(".hook-scope-global");
    expect(badge).not.toBeNull();
    expect(badge?.textContent).toBe("Global");
    // The path rides the badge tooltip; the file lives in the blocked HOME
    // tree so there is no open-in-editor affordance.
    expect(badge?.getAttribute("data-tooltip")).toContain("~/.kiro/hooks/greet.json");
    expect(row.querySelector(".hook-open")).toBeNull();
  });

  it("workspace rows carry no scope badge and keep the open button", async () => {
    mockGet.mockResolvedValue({ hooks: [cmdHook({ scope: "workspace" })] });
    loadHooks();
    await flush();
    const row = list().querySelector(".hook-row") as HTMLElement;
    expect(row.querySelector(".hook-scope-global")).toBeNull();
    expect(row.querySelector(".hook-open")).not.toBeNull();
  });

  it("shows the empty state for no hooks", async () => {
    mockGet.mockResolvedValue({ hooks: [] });
    loadHooks();
    await flush();
    expect(list().textContent).toContain("No hooks yet.");
  });

  it("shows an error state on fetch failure", async () => {
    mockGet.mockResolvedValue(null);
    loadHooks();
    await flush();
    expect(list().textContent).toContain("Couldn't load hooks.");
  });
});

describe("toggle flow", () => {
  it("dispatches hooks.set_enabled with the new state and refetches", async () => {
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook({ enabled: true })] });
    loadHooks();
    await flush();
    mockSetEnabled.mockResolvedValue(undefined);

    const toggle = list().querySelector(".hook-toggle") as HTMLInputElement;
    toggle.checked = false;
    toggle.dispatchEvent(new Event("change", { bubbles: true }));
    await flush();

    expect(mockSetEnabled).toHaveBeenCalledWith({ id: "id-greet", enabled: false });
    // refetch fired (initial load + post-toggle reconcile)
    expect(mockGet.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});

describe("run flow", () => {
  it("dispatches hooks.run and shows the captured output + exit code", async () => {
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();
    mockRun.mockResolvedValue({ output: "hello\n", exit_code: 0, ran: true });

    (list().querySelector(".hook-run") as HTMLButtonElement).click();
    await flush();

    expect(mockRun).toHaveBeenCalledWith({ id: "id-greet" });
    const out = list().querySelector(".hook-output");
    expect(out).not.toBeNull();
    expect(out?.textContent).toContain("hello");
    expect(out?.textContent).toContain("exit 0");
  });

  it("surfaces a non-zero exit code", async () => {
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();
    mockRun.mockResolvedValue({ output: "boom", exit_code: 2, ran: true });

    (list().querySelector(".hook-run") as HTMLButtonElement).click();
    await flush();

    expect(list().querySelector(".hook-output")?.textContent).toContain("exit 2");
  });

  it("toasts and shows no output panel when the run action fails", async () => {
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();
    mockRun.mockResolvedValue(null);

    (list().querySelector(".hook-run") as HTMLButtonElement).click();
    await flush();

    expect(vi.mocked(showToast)).toHaveBeenCalled();
    expect(list().querySelector(".hook-output")).toBeNull();
  });
});

describe("open file", () => {
  it("opens the hook file in the editor", async () => {
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();

    (list().querySelector(".hook-open") as HTMLButtonElement).click();
    expect(vi.mocked(openFile)).toHaveBeenCalledWith(".kiro/hooks/greet.json");
  });
});

describe("SSE refetch", () => {
  it("refetches (debounced) when a hooks_changed event arrives", async () => {
    initHooks();
    const handler = vi.mocked(onSSE).mock.calls.find((c) => c[0] === "hooks_changed")?.[1];
    expect(handler).toBeDefined();
    mockGet.mockClear();
    mockGet.mockResolvedValue({ hooks: [] });
    (handler as () => void)();
    await vi.advanceTimersByTimeAsync(300);
    expect(mockGet).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Row signature (keyenc `join`, both nesting levels).
//
// The signature only gates whether a row's children are rebuilt — row identity
// is `hook:${id}`, so a collision leaves a STALE ROW, not a missing or wrong
// one. `command`, `prompt`, `matcher` come from the hook file and the run
// `output` is captured process output, so the old "\u00a7" / "|" separators
// were merely unlikely, not reserved.
// ---------------------------------------------------------------------------

describe("hook row signature", () => {
  const SECTION = "\u00a7";

  /** The signature expression as it was before the keyenc adoption. */
  function oldSig(h: HookLike, out?: { ran: boolean; exit_code: number; output: string }): string {
    const outSig = out ? `${out.ran ? "1" : "0"}|${String(out.exit_code)}|${out.output}` : "";
    return [
      h.enabled ? "1" : "0",
      h.trigger,
      h.action_type,
      h.scope ?? "",
      h.command ?? "",
      h.prompt ?? "",
      h.matcher ?? "",
      h.disabled_reason ?? "",
      outSig,
    ].join(SECTION);
  }

  async function sigFor(h: HookLike): Promise<string> {
    mockGet.mockResolvedValue({ hooks: [h] });
    loadHooks();
    await flush();
    return list().querySelector(".hook-row")?.getAttribute("data-sig") ?? "";
  }

  it("distinguishes two states the old \u00a7-joined signature collapsed", async () => {
    // `command` and `prompt` are adjacent free-form fields, so a section sign
    // inside the command could impersonate the boundary before the prompt.
    const a = cmdHook({ command: `echo a${SECTION}b`, prompt: "c" });
    const b = cmdHook({ command: "echo a", prompt: `b${SECTION}c` });
    expect(oldSig(a)).toBe(oldSig(b));

    // Same hook id, so the second load reuses the row and rewrites data-sig
    // only when the signature actually changed.
    expect(await sigFor(a)).not.toBe(await sigFor(b));
  });

  it("emits verbatim components for ordinary input", async () => {
    const sig = await sigFor(cmdHook({ matcher: "*.ts" }));
    expect(sig).toBe("1:Manual:runCommand::echo hello::*.ts::");
    // Nine components, one per field, none of them escaped.
    expect(split(sig)).toEqual(["1", "Manual", "runCommand", "", "echo hello", "", "*.ts", "", ""]);
  });

  it("nests the run-output signature as ONE component of the row signature", async () => {
    // An output carrying the row separator used to add a field to the row
    // signature; nested through its own join it stays a single component.
    initHooks();
    mockGet.mockResolvedValue({ hooks: [cmdHook()] });
    loadHooks();
    await flush();
    mockRun.mockResolvedValue({ output: `x${SECTION}y:z`, exit_code: 0, ran: true });

    (list().querySelector(".hook-run") as HTMLButtonElement).click();
    await flush();

    const sig = list().querySelector(".hook-row")?.getAttribute("data-sig") ?? "";
    const parts = split(sig);
    expect(parts).toHaveLength(9);
    // The 9th component is exactly the inner join of the three output fields,
    // recoverable in full — the outer level never saw its contents.
    expect(parts[8]).toBe(join("1", "0", `x${SECTION}y:z`));
    expect(split(parts[8] ?? "")).toEqual(["1", "0", `x${SECTION}y:z`]);
  });

  it("keeps a hook with a run output distinct from one whose text mimics it", async () => {
    const withOutput = cmdHook();
    const mimic = cmdHook({ disabled_reason: `1|0|hello${SECTION}` });

    initHooks();
    mockGet.mockResolvedValue({ hooks: [withOutput] });
    loadHooks();
    await flush();
    mockRun.mockResolvedValue({ output: "hello", exit_code: 0, ran: true });
    (list().querySelector(".hook-run") as HTMLButtonElement).click();
    await flush();
    const sigWithOutput = list().querySelector(".hook-row")?.getAttribute("data-sig") ?? "";

    _resetForTest();
    seedDom();
    const sigMimic = await sigFor(mimic);

    expect(sigWithOutput).not.toBe(sigMimic);
  });
});
