// @vitest-environment happy-dom
// Accessibility tests: verify missing labels, focus management, and keyboard nav fixes.
import { describe, it, expect, vi } from "vitest";
import { domRenderer } from "./smd-renderer.js";
import { CHECKBOX } from "./smd-parser-types.js";

describe("a11y: missing labels", () => {
  it("smd-renderer CHECKBOX has aria-label", () => {
    const container = document.createElement("div");
    const renderer = domRenderer(container, false);
    renderer.add_token(renderer.data, CHECKBOX);
    const cb = container.querySelector("input[type=checkbox]") as HTMLInputElement;
    expect(cb).not.toBeNull();
    expect(cb.getAttribute("aria-label")).toBe("Task item");
  });

  it("toast element has aria-label with level and message", async () => {
    const { showToast } = await import("./toast.js");
    const dismiss = showToast("File saved", "success", 5000);
    const toast = document.querySelector(".vk-toast-success") as HTMLDivElement;
    expect(toast).not.toBeNull();
    expect(toast.getAttribute("aria-label")).toBe("success notification: File saved. Click to dismiss.");
    dismiss();
  });

  it("banner stack container has aria-label and aria-live", async () => {
    // Setup minimal DOM for banner-stack
    const container = document.createElement("div");
    container.id = "banner-stack";
    document.body.appendChild(container);

    const { renderStack } = await import("./banner-stack.js");
    renderStack();

    expect(container.getAttribute("aria-label")).toBe("Notifications");
    expect(container.getAttribute("aria-live")).toBe("polite");
    document.body.removeChild(container);
  });

  it("settings tab bar gets role=tablist and buttons get role=tab", async () => {
    // Setup minimal DOM for settings-tabs
    const bar = document.createElement("div");
    bar.id = "settings-tab-bar";
    const select = document.createElement("select");
    select.id = "settings-tab-select";
    const tabs = ["general", "tools", "permissions", "instructions", "git"];
    for (const t of tabs) {
      const btn = document.createElement("button");
      btn.setAttribute("data-settings-tab", t);
      bar.appendChild(btn);
      const opt = document.createElement("option");
      opt.value = t;
      select.appendChild(opt);
    }
    document.body.append(bar, select);

    const { initSettingsTabs } = await import("./settings-tabs.js");
    initSettingsTabs();

    expect(bar.getAttribute("role")).toBe("tablist");
    expect(bar.getAttribute("aria-label")).toBe("Settings sections");
    const generalBtn = bar.querySelector('[data-settings-tab="general"]') as HTMLButtonElement;
    expect(generalBtn.getAttribute("role")).toBe("tab");
    expect(generalBtn.getAttribute("aria-label")).toBe("General");
    expect(generalBtn.getAttribute("aria-controls")).toBe("settings-panel-general");
    expect(generalBtn.id).toBe("settings-tab-general");
    expect(generalBtn.getAttribute("tabindex")).toBe("0");
    const toolsBtn = bar.querySelector('[data-settings-tab="tools"]') as HTMLButtonElement;
    expect(toolsBtn.getAttribute("tabindex")).toBe("-1");

    document.body.removeChild(bar);
    document.body.removeChild(select);
  });
});

describe("a11y: keyboard navigation on picker grid", () => {
  it("wireArrowNav makes items focusable via tabindex", async () => {
    const { wireArrowNav } = await import("./arrow-nav.js");
    const container = document.createElement("div");
    const btn1 = document.createElement("button");
    btn1.className = "picker-btn";
    btn1.textContent = "Model A";
    const btn2 = document.createElement("button");
    btn2.className = "picker-btn";
    btn2.textContent = "Model B";
    container.append(btn1, btn2);
    document.body.appendChild(container);

    wireArrowNav(container, ".picker-btn", { orientation: "horizontal" });

    expect(btn1.getAttribute("tabindex")).toBe("0");
    expect(btn2.getAttribute("tabindex")).toBe("-1");

    document.body.removeChild(container);
  });
});

describe("a11y: focus management", () => {
  it("overflow menu returns focus to trigger on close", async () => {
    const { openOverflowMenu, closeOverflowMenu } = await import("./overflow-menu.js");
    const trigger = document.createElement("button");
    trigger.textContent = "Menu";
    document.body.appendChild(trigger);
    trigger.focus();

    openOverflowMenu(trigger, [
      { id: "a", label: "Action A", onSelect: vi.fn() },
    ]);

    // Focus moved to menu item
    const menuItem = document.querySelector(".overflow-menu-item") as HTMLButtonElement;
    expect(menuItem).not.toBeNull();

    closeOverflowMenu();

    expect(document.activeElement).toBe(trigger);
    document.body.removeChild(trigger);
  });
});

describe("a11y: aria-expanded on popover triggers", () => {
  it("commands-menu sets aria-expanded on prompt input when popover opens/closes", async () => {
    vi.resetModules();
    const input = document.createElement("textarea");
    vi.doMock("./store.js", () => ({
      getActive: () => ({
        available_commands: [{ name: "/help", description: "Help" }],
        available_prompts: [],
      }),
      getActiveId: () => "chat1",
    }));
    vi.doMock("./api-client.js", () => ({ apiGet: vi.fn(async () => null) }));
    vi.doMock("./dom.js", () => ({
      $: new Proxy({ promptInput: input }, {
        get: (t, p) => (p in t ? (t as Record<string, unknown>)[p as string] : document.createElement("div")),
      }),
    }));
    const { initCommandsMenu } = await import("./commands-menu.js");
    initCommandsMenu();

    input.value = "/";
    input.dispatchEvent(new Event("input"));
    expect(input.getAttribute("aria-expanded")).toBe("true");

    input.value = "hello";
    input.dispatchEvent(new Event("input"));
    expect(input.getAttribute("aria-expanded")).toBe("false");
  });

  it("supervised-pill sets aria-expanded on expand/collapse", async () => {
    vi.resetModules();
    vi.doMock("./store.js", () => ({
      getActive: () => ({
        id: "s1",
        supervised_mode: true,
        pending_changes: [],
        messages: [],
      }),
      version: { value: 1 },
    }));
    vi.doMock("./signals.js", () => ({ effect: (fn: () => void) => fn() }));
    vi.doMock("./actions/chat.js", () => ({
      setSupervised: { dispatch: vi.fn() },
      resolveAllPending: { dispatch: vi.fn() },
      resolvePendingChange: { dispatch: vi.fn() },
      trustPending: { dispatch: vi.fn() },
      clearPendingTrust: { dispatch: vi.fn() },
    }));
    vi.doMock("./actions/index.js", () => ({
      bindLoadingState: () => () => {},
      bindLoadingStateMulti: () => () => {},
    }));
    vi.doMock("./editor-openers.js", () => ({ openPendingDiff: vi.fn() }));
    vi.doMock("./pill-expand.js", () => ({
      makeExpandable: (_pill: HTMLElement, _content: HTMLElement, opts?: { onExpand?: () => void; onCollapse?: () => void }) => {
        _pill.addEventListener("click", () => {
          if (_pill.classList.contains("pill-expanded")) {
            _pill.classList.remove("pill-expanded");
            opts?.onCollapse?.();
          } else {
            _pill.classList.add("pill-expanded");
            opts?.onExpand?.();
          }
        });
      },
      collapseAll: vi.fn(),
    }));

    const pill = document.createElement("div");
    pill.id = "supervised-pill";
    const label = document.createElement("span");
    label.className = "pill-label";
    pill.appendChild(label);
    const content = document.createElement("div");
    content.className = "pill-expand-content";
    pill.appendChild(content);
    document.body.appendChild(pill);

    const { initSupervisedPill } = await import("./supervised-pill.js");
    initSupervisedPill();

    expect(pill.getAttribute("aria-expanded")).toBe("false");

    pill.click();
    expect(pill.getAttribute("aria-expanded")).toBe("true");

    pill.click();
    expect(pill.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(pill);
  });

  it("auto-approve button has aria-label matching title", async () => {
    vi.resetModules();
    const btn = document.createElement("button");
    btn.id = "auto-approve-crew-btn";
    vi.doMock("./dom.js", () => ({
      $: new Proxy({ autoApproveCrewBtn: btn }, {
        get: (t, p) => (p in t ? (t as Record<string, unknown>)[p as string] : document.createElement("div")),
      }),
    }));
    vi.doMock("./store.js", () => ({
      getActive: () => ({
        id: "s1",
        auto_approve_crew: true,
        messages: [{ event_kind: "crew" }],
      }),
      version: { value: 1 },
    }));
    vi.doMock("./signals.js", () => ({ effect: (fn: () => void) => fn() }));
    vi.doMock("./actions/chat.js", () => ({ setAutoApproveCrew: { dispatch: vi.fn() } }));
    vi.doMock("./actions/index.js", () => ({ bindLoadingState: () => () => {} }));

    const { initAutoApprove } = await import("./auto-approve.js");
    initAutoApprove();

    expect(btn.getAttribute("aria-label")).toBe("Auto-approve subagent tools (on)");
    expect(btn.getAttribute("aria-pressed")).toBe("true");
  });
});

describe("a11y: tool-card aria-expanded on toggle", () => {
  it("tool-toggle button starts with aria-expanded=false and aria-label", async () => {
    vi.resetModules();
    vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
    vi.mock("./editor-openers.js", () => ({ openFile: () => {}, openFileDiff: () => {} }));
    vi.mock("./tool-group.js", () => ({ trackInProgress: () => {} }));

    const { buildToolCard } = await import("./tool-card.js");
    const el = buildToolCard({
      id: "t1",
      title: "Running: grep",
      kind: "tool_use",
      status: "completed",
      input: { pattern: "foo" },
      live: false,
    });

    const toggle = el.querySelector(".tool-toggle") as HTMLButtonElement;
    expect(toggle).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-label")).toBe("Toggle tool details");
  });

  it("tool-toggle aria-expanded toggles on click", async () => {
    vi.resetModules();
    vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
    vi.mock("./editor-openers.js", () => ({ openFile: () => {}, openFileDiff: () => {} }));
    vi.mock("./tool-group.js", () => ({ trackInProgress: () => {} }));

    const { buildToolCard } = await import("./tool-card.js");
    const el = buildToolCard({
      id: "t2",
      title: "Running: grep",
      kind: "tool_use",
      status: "completed",
      input: { pattern: "bar" },
      live: false,
    });
    document.body.appendChild(el);

    const toggle = el.querySelector(".tool-toggle") as HTMLButtonElement;
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(el);
  });
});

describe("a11y: async-button screen reader announcements", () => {
  it("announces 'Action completed' on success via aria-live region", async () => {
    vi.useFakeTimers();
    const { withAsyncFeedback } = await import("./async-button.js");
    const btn = document.createElement("button");
    btn.innerHTML = "Go";
    document.body.appendChild(btn);

    await withAsyncFeedback(btn, () => Promise.resolve());

    // The sr-only live region should exist (use span selector to avoid banner-stack)
    const liveEl = document.querySelector("span.sr-only[aria-live='polite']") as HTMLElement;
    expect(liveEl).not.toBeNull();

    // After the 50ms delay, the announcement text is set
    await vi.advanceTimersByTimeAsync(50);
    expect(liveEl.textContent).toBe("Action completed");

    await vi.advanceTimersByTimeAsync(1200);
    document.body.removeChild(btn);
    vi.useRealTimers();
  });

  it("announces 'Action failed' on error via aria-live region", async () => {
    vi.useFakeTimers();
    const { withAsyncFeedback } = await import("./async-button.js");
    const btn = document.createElement("button");
    btn.innerHTML = "Go";
    document.body.appendChild(btn);

    await withAsyncFeedback(btn, () => Promise.reject(new Error("oops")));

    // Advance past the 50ms announce delay
    await vi.advanceTimersByTimeAsync(50);
    const liveEl = document.querySelector("span.sr-only[aria-live='polite']") as HTMLElement;
    expect(liveEl).not.toBeNull();
    expect(liveEl.textContent).toBe("Action failed");

    await vi.advanceTimersByTimeAsync(1200);
    document.body.removeChild(btn);
    vi.useRealTimers();
  });
});
