// @vitest-environment happy-dom
// Accessibility tests: verify missing labels, focus management, and keyboard nav fixes.
import { describe, it, expect, vi } from "vitest";
import { domRenderer } from "./smd-renderer.js";
import { CHECKBOX } from "./smd-parser-types.js";

describe("a11y: missing labels", () => {
  it("smd-renderer CHECKBOX has aria-label", () => {
    const container = document.createElement("div");
    const renderer = domRenderer(container, { animateText: false });
    renderer.add_token(renderer.data, CHECKBOX);
    const cb = container.querySelector("input[type=checkbox]")!;
    expect(cb).not.toBeNull();
    expect(cb.getAttribute("aria-label")).toBe("Task item");
  });

  it("toast is announced via the shared live region and is self-describing", async () => {
    const { showToast, _resetForTest } = await import("./toast.js");
    const dismiss = showToast("File saved", "success", 5000);
    const toast = document.querySelector(".uip-toast--success")!;
    expect(toast).not.toBeNull();
    // The library decouples announcement from the visual node: the toast is
    // keyboard-focusable with a visually-hidden dismiss hint, and the message
    // is announced through the shared polite live region rather than an
    // aria-label on the node (which would double-announce / nest live regions).
    expect(toast.getAttribute("tabindex")).toBe("0");
    expect(toast.querySelector(".uip-visually-hidden")?.textContent).toBe("Click to dismiss.");
    expect(toast.querySelector(".uip-toast-msg")?.textContent).toBe("File saved");
    expect(document.querySelector('[aria-live="polite"]')).not.toBeNull();
    dismiss();
    _resetForTest();
  });

  it("banner stack container has aria-label and aria-live", async () => {
    vi.resetModules();
    vi.doUnmock("./dom.js");
    vi.doUnmock("./store.js");
    vi.doUnmock("./actions/index.js");
    vi.doUnmock("./api-client.js");
    vi.doUnmock("./signals.js");
    // Setup minimal DOM for banner-stack
    const container = document.createElement("div");
    container.id = "banner-stack";
    document.body.appendChild(container);

    const { ensureBound } = await import("./banner-stack.js");
    ensureBound();

    expect(container.getAttribute("aria-label")).toBe("Notifications");
    expect(container.getAttribute("aria-live")).toBe("polite");
    document.body.removeChild(container);
  });

  it("settings tab bar gets role=tablist and buttons get role=tab", async () => {
    vi.resetModules();
    vi.doUnmock("./dom.js");
    vi.doUnmock("./store.js");
    vi.doUnmock("./actions/index.js");
    vi.doUnmock("./api-client.js");
    vi.doUnmock("./signals.js");
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
    const generalBtn = bar.querySelector('[data-settings-tab="general"]')!;
    expect(generalBtn.getAttribute("role")).toBe("tab");
    expect(generalBtn.getAttribute("aria-label")).toBe("General");
    expect(generalBtn.getAttribute("aria-controls")).toBe("settings-panel-general");
    expect(generalBtn.id).toBe("settings-tab-general");
    expect(generalBtn.getAttribute("tabindex")).toBe("0");
    const toolsBtn = bar.querySelector('[data-settings-tab="tools"]')!;
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

describe("a11y: aria-expanded on popover triggers", () => {
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
      sessionsVersion: { value: 1 },
      activeVersion: { value: 1 },
      activeSession: {
        value: { id: "s1", supervised_mode: true, pending_changes: [], messages: [] },
      },
      messagesVersion: { value: 1 },
    }));
    vi.doMock("./signals.js", () => ({
      effect: (fn: () => void) => {
        fn();
      },
    }));
    vi.doMock("./actions/chat.js", () => ({
      setSupervised: { dispatch: vi.fn() },
      resolveAllPending: { dispatch: vi.fn() },
      resolvePendingChange: { dispatch: vi.fn() },
      trustPending: { dispatch: vi.fn() },
      clearPendingTrust: { dispatch: vi.fn() },
    }));
    vi.doMock("./actions/index.js", () => ({
      bindLoadingState: () => () => {
        /* noop */
      },
      registerCleanup: () => {
        /* noop */
      },
    }));
    vi.doMock("./editor-openers.js", () => ({ openPendingDiff: vi.fn() }));
    vi.doMock("./pill-expand.js", () => ({
      makeExpandable: (
        _pill: HTMLElement,
        _content: HTMLElement,
        opts?: { onExpand?: () => void; onCollapse?: () => void },
      ) => {
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
});

describe("a11y: tool-card aria-expanded on toggle", () => {
  it("tool-toggle button starts with aria-expanded=false and aria-label", async () => {
    vi.resetModules();
    vi.mock("./scroll.js", () =>
      import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
    );
    vi.mock("./editor-openers.js", () => ({
      openFile: () => {
        /* noop */
      },
      openFileDiff: () => {
        /* noop */
      },
    }));
    vi.mock("./tool-group.js", () => ({
      trackInProgress: () => {
        /* noop */
      },
    }));

    const { buildToolCard } = await import("./tool-card.js");
    const el = buildToolCard({
      id: "t1",
      title: "Running: grep",
      kind: "tool_use",
      status: "completed",
      input: { pattern: "foo" },
      live: false,
    });

    const toggle = el.querySelector<HTMLElement>(".tool-toggle")!;
    expect(toggle).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-label")).toBe("Toggle tool details");
  });

  it("tool-toggle aria-expanded toggles on click", async () => {
    vi.resetModules();
    vi.mock("./scroll.js", () =>
      import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
    );
    vi.mock("./editor-openers.js", () => ({
      openFile: () => {
        /* noop */
      },
      openFileDiff: () => {
        /* noop */
      },
    }));
    vi.mock("./tool-group.js", () => ({
      trackInProgress: () => {
        /* noop */
      },
    }));

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

    const toggle = el.querySelector<HTMLElement>(".tool-toggle")!;
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(el);
  });
});

describe("a11y: confirm dialog focus management", () => {
  it("destructive variant focuses cancel button", async () => {
    const { confirm } = await import("./confirm.js");
    const p = confirm("Delete everything?", "Delete", "destructive");
    const dialog = document.querySelector(".uip-confirm")!;
    expect(dialog).not.toBeNull();
    const cancelBtn = dialog.querySelector<HTMLElement>(".uip-confirm-cancel")!;
    expect(document.activeElement).toBe(cancelBtn);
    // Close the dialog to resolve the promise
    cancelBtn.click();
    expect(await p).toBe(false);
  });

  it("normal variant focuses the confirm button", async () => {
    const { confirm } = await import("./confirm.js");
    const p = confirm("Continue?", "OK", "normal");
    const dialog = document.querySelector(".uip-confirm")!;
    const okBtn = dialog.querySelector<HTMLElement>(".uip-confirm-ok")!;
    // The library focuses the primary (confirm) action for non-destructive
    // prompts; destructive prompts focus Cancel instead (see the test above).
    expect(document.activeElement).toBe(okBtn);
    okBtn.click();
    expect(await p).toBe(true);
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
    const liveEl = document.querySelector("span.sr-only[aria-live='polite']")!;
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
    const liveEl = document.querySelector("span.sr-only[aria-live='polite']")!;
    expect(liveEl).not.toBeNull();
    expect(liveEl.textContent).toBe("Action failed");

    await vi.advanceTimersByTimeAsync(1200);
    document.body.removeChild(btn);
    vi.useRealTimers();
  });
});

describe("a11y: failed tool aria-expanded", () => {
  it("failed status sets aria-expanded=true on the tool toggle", async () => {
    vi.resetModules();
    const noop = (): void => {
      /* noop */
    };
    vi.doMock("./scroll.js", () => ({ setUserScrolledUp: noop }));
    vi.doMock("./editor-openers.js", () => ({ openFile: noop, openFileDiff: noop }));
    vi.doMock("./tool-group.js", () => ({
      trackInProgress: noop,
      untrackInProgress: noop,
      maybeCollapseGroup: noop,
      formatDuration: (ms: number) => String(ms),
    }));
    vi.doMock("./messages-actions.js", () => ({ addEditActions: noop }));
    vi.doMock("./actions/index.js", () => ({ bindLoadingState: () => () => undefined }));

    const { buildToolCard } = await import("./tool-card.js");
    const { initToolCallbacks, updateToolCall } = await import("./messages-tools.js");

    initToolCallbacks({
      pushBind: noop,
      svgTemplate: () => () => document.createElement("span"),
      refreshGroupHeader: noop,
      explainError: () => Promise.resolve(""),
    });

    const card = buildToolCard({
      id: "tf1",
      title: "Running: grep",
      kind: "other",
      status: "in_progress",
      input: { pattern: "boom" },
      live: true,
    });
    document.body.appendChild(card);

    const toggle = card.querySelector<HTMLElement>(".tool-toggle")!;
    const details = card.querySelector<HTMLElement>(".tool-details")!;
    // Precondition: a live (in_progress) medium-tier card starts collapsed.
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(details.classList.contains("collapsed")).toBe(true);

    updateToolCall(card, {
      id: "tf1",
      title: "Running: grep",
      kind: "other",
      status: "failed",
      ts: 0,
    });

    // The failed branch auto-expands the details AND must keep the toggle's
    // aria-expanded in sync (mirrors wireToggle's expand path).
    expect(details.classList.contains("collapsed")).toBe(false);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    document.body.removeChild(card);
    vi.doUnmock("./scroll.js");
    vi.doUnmock("./editor-openers.js");
    vi.doUnmock("./tool-group.js");
    vi.doUnmock("./subagent.js");
    vi.doUnmock("./messages-actions.js");
    vi.doUnmock("./actions/index.js");
  });
});
