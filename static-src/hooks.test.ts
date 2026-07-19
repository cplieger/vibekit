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
