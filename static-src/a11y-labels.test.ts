// Accessibility tests: verify missing labels, focus management, and keyboard nav fixes.
import { describe, it, expect, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
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
    // ("git" removed: the Git & forges settings tab was retired.)
    const tabs = ["general", "tools", "permissions", "instructions"];
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

describe("a11y: permissions rule-form labels (static markup)", () => {
  // The Permissions-panel adders (Active policy, Test a decision, Agent
  // ignore — the legacy Command-rules adder was removed with the P8 Cedar
  // migration) were reworked from unlabeled chip rows into labeled
  // .rule-form grids (audit C7). Guard the real markup: every control sits
  // inside a <label> that carries a visible .rf-label, and the submit
  // affordance is a labeled button, not an icon-only pill.
  it("every rule-form control has a visible label and a labeled submit button", () => {
    // Parse only the permissions-panel slice (comment marker to the next
    // panel's marker): a full-document parse would make the runner chase the
    // <link rel=stylesheet> over the network.
    const start = indexHtml.indexOf("<!-- Permissions:");
    const end = indexHtml.indexOf("<!-- Instructions:");
    expect(start).toBeGreaterThan(-1);
    expect(end).toBeGreaterThan(start);
    const doc = document.createElement("div");
    doc.innerHTML = indexHtml.slice(start, end);

    const forms = Array.from(doc.querySelectorAll<HTMLElement>(".rule-form"));
    const kinds = forms.map((f) => f.getAttribute("data-rule-form"));
    expect(kinds.sort()).toEqual(["explain", "ignore", "policy"]);

    for (const form of forms) {
      // Grouped + named for assistive tech.
      expect(form.getAttribute("role")).toBe("group");
      expect(form.getAttribute("aria-label")).toBeTruthy();

      for (const control of Array.from(form.querySelectorAll("select, input"))) {
        const label = control.closest("label");
        expect(label, `control #${control.id} must be wrapped in a <label>`).not.toBeNull();
        const labelText = label?.querySelector(".rf-label")?.textContent?.trim() ?? "";
        expect(labelText, `control #${control.id} needs visible label text`).not.toBe("");
      }

      const submit = form.querySelector<HTMLButtonElement>("button.rf-submit");
      expect(submit).not.toBeNull();
      expect(submit?.textContent?.trim(), "submit must carry a visible text label").not.toBe("");
    }

    // The permissions-ui.ts ids the controllers bind to must all survive the
    // markup rework (the redesign keeps ids stable so the TS needs no
    // structural changes).
    for (const id of [
      "native-rule-scope",
      "native-rule-capability",
      "native-rule-effect",
      "native-rule-match",
      "native-rule-exclude",
      "native-rule-add",
      "native-explain-capability",
      "native-explain-resource",
      "native-explain-run",
      "agent-ignore-input",
      "agent-ignore-add",
    ]) {
      expect(doc.querySelector(`[id="${id}"]`), `#${id} must exist`).not.toBeNull();
    }
  });
});

describe("a11y: keyboard navigation on picker grid", () => {
  it("rovingFocus makes items focusable via tabindex", async () => {
    const { rovingFocus } = await import("@cplieger/ui-primitives/roving-focus");
    const container = document.createElement("div");
    const btn1 = document.createElement("button");
    btn1.className = "picker-btn";
    btn1.textContent = "Model A";
    const btn2 = document.createElement("button");
    btn2.className = "picker-btn";
    btn2.textContent = "Model B";
    container.append(btn1, btn2);
    document.body.appendChild(container);

    rovingFocus(container, ".picker-btn", { orientation: "horizontal" });

    expect(btn1.getAttribute("tabindex")).toBe("0");
    expect(btn2.getAttribute("tabindex")).toBe("-1");

    document.body.removeChild(container);
  });
});

// The supervised-pill aria-expanded test is gone with the pill. It also could
// not fail: it mocked makeExpandable and then asserted the mock's own
// aria-expanded flip. That contract belongs to @cplieger/ui-primitives'
// createPopup, which owns and tests it.

describe("a11y: tool-card aria-expanded on toggle", () => {
  it("tool-disclosure button starts with aria-expanded=false and aria-label", async () => {
    vi.resetModules();
    vi.doMock("./scroll.js", () =>
      import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
    );
    vi.doMock("./editor-openers.js", () => ({
      openFile: () => {
        /* noop */
      },
      openFileDiff: () => {
        /* noop */
      },
      // navigate.ts imports this name, and Browser Mode links ESM for real
      // rather than reading properties off a namespace object, so it has to
      // exist. `undefined` is what the node runner gave it.
      openFileGitDiff: undefined,
    }));
    vi.doMock("./tool-group.js", () => ({
      // The same stubs the sibling doMock below installs for this module. Under
      // Browser Mode's real linking every name tool-card.ts imports has to exist,
      // and these three are CALLED on the path under test, so they are noops
      // rather than undefined.
      untrackInProgress: () => {
        /* noop */
      },
      maybeCollapseGroup: () => {
        /* noop */
      },
      formatDuration: (ms: number) => String(ms),
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

    const toggle = el.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(toggle).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-label")).toBe("Toggle tool details");
  });

  it("tool-disclosure aria-expanded toggles on click", async () => {
    vi.resetModules();
    vi.doMock("./scroll.js", () =>
      import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
    );
    vi.doMock("./editor-openers.js", () => ({
      openFile: () => {
        /* noop */
      },
      openFileDiff: () => {
        /* noop */
      },
      // navigate.ts imports this name, and Browser Mode links ESM for real
      // rather than reading properties off a namespace object, so it has to
      // exist. `undefined` is what the node runner gave it.
      openFileGitDiff: undefined,
    }));
    vi.doMock("./tool-group.js", () => ({
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

    const toggle = el.querySelector<HTMLElement>(".tool-disclosure")!;
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
    const dialog = document.querySelector(".uip-ask")!;
    expect(dialog).not.toBeNull();
    const cancelBtn = dialog.querySelector<HTMLElement>(".uip-ask-cancel")!;
    expect(document.activeElement).toBe(cancelBtn);
    // Close the dialog to resolve the promise
    cancelBtn.click();
    expect(await p).toBe(false);
  });

  it("normal variant focuses the confirm button", async () => {
    const { confirm } = await import("./confirm.js");
    const p = confirm("Continue?", "OK", "normal");
    const dialog = document.querySelector(".uip-ask")!;
    const okBtn = dialog.querySelector<HTMLElement>(".uip-ask-ok")!;
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
    vi.doMock("./editor-openers.js", () => ({
      openFile: noop,
      openFileDiff: noop,
      openFileGitDiff: undefined,
    }));
    vi.doMock("./tool-group.js", () => ({
      trackInProgress: noop,
      untrackInProgress: noop,
      maybeCollapseGroup: noop,
      formatDuration: (ms: number) => String(ms),
    }));
    vi.doMock("./actions/index.js", () => ({
      // `registerCleanup` is CALLED on this path, so it is a noop rather than
      // undefined; Browser Mode's real linking requires the name either way.
      registerCleanup: () => {
        /* noop */
      },
      bindLoadingState: () => () => undefined,
    }));

    const { buildToolCard } = await import("./tool-card.js");
    const { initToolCallbacks, updateToolCall } = await import("./messages-tools.js");

    initToolCallbacks({
      pushBind: noop,
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

    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    const details = card.querySelector<HTMLElement>(".tool-details")!;
    // Precondition: a live (in_progress) medium-tier card starts collapsed —
    // the disclosure controller marks the region aria-hidden + inert.
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(details.getAttribute("aria-hidden")).toBe("true");

    updateToolCall(card, {
      id: "tf1",
      title: "Running: grep",
      kind: "other",
      status: "failed",
      ts: 0,
    });

    // The failed branch auto-expands the details (expandToolDetails → the
    // disclosure controller), which must keep the toggle's aria-expanded and
    // the region's aria-hidden in sync.
    expect(details.getAttribute("aria-hidden")).toBe("false");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    document.body.removeChild(card);
    vi.doUnmock("./scroll.js");
    vi.doUnmock("./editor-openers.js");
    vi.doUnmock("./tool-group.js");
    vi.doUnmock("./subagent.js");
    vi.doUnmock("./actions/index.js");
  });
});

describe("a11y: History row accessible names", () => {
  // A history row's OPEN control is a real button whose name says what a click
  // does ("Open X"); the row itself carries no role, because a role="button" on it
  // is Children-Presentational and flattens the delete button beside it out of the
  // accessibility tree (axe nested-interactive, serious, every row).
  // A settled parentless run also states its OUTCOME, and that outcome is a
  // glyph — so the word has exactly one home, that control's accessible name, and
  // must not be duplicated as visible text beside the glyph it replaced.
  it("a settled run row names the outcome once, in the accessible name only", async () => {
    vi.resetModules();
    const noop = (): void => {
      /* noop */
    };
    vi.doMock("./actions/chat.js", () => ({
      loadSessions: {
        dispatch: () =>
          Promise.resolve({
            sessions: [],
            runs: [
              { workflow_id: "wf_a11y", name: "nightly-sweep", status: "failed", updated_at: 1 },
            ],
          }),
        cancel: noop,
      },
      deleteChat: { dispatch: noop },
    }));
    vi.doMock("./actions/chat-search.js", () => ({
      searchChats: { dispatch: noop, cancel: noop },
    }));
    // The row's delete control: two actions plus the confirm dialog, stubbed for
    // the same reason the others are — actions/index.js is reduced to one symbol
    // here, so an unmocked action def cannot build.
    vi.doMock("./actions/runs.js", () => ({ deleteRun: { dispatch: noop } }));
    vi.doMock("./confirm.js", () => ({ confirm: async () => false }));
    vi.doMock("./actions/index.js", () => ({ registerCleanup: noop }));
    vi.doMock("./chat.js", () => ({ openPreviousSession: noop, openChatTab: noop }));
    vi.doMock("./run-view.js", () => ({ openRunView: noop }));
    vi.doMock("./tabs.js", () => ({
      // The toggle takes no callback now: the tab factory owns the page's loader.
      // The suite mounts the page itself, so this only has to resolve.
      toggleHistoryView: () => Promise.resolve(),
      hasTab: () => false,
    }));
    vi.doMock("@cplieger/ui-primitives/skeleton", () => ({
      skeletonTiming: () => ({ cancel: noop }),
    }));
    vi.doMock("./editor-openers.js", () => ({
      openFileDiff: noop,
      openFile: undefined,
      openFileGitDiff: undefined,
    }));
    vi.doMock("./navigate.js", () => ({ openChange: noop, openAtLine: noop }));
    vi.doMock("./scroll.js", () => ({
      setUserScrolledUp: noop,
      preserveReadingPosition: (fn: () => void) => {
        fn();
      },
    }));
    vi.doMock("./tool-group.js", () => ({ trackInProgress: noop }));

    const host = document.createElement("div");
    host.id = "history-table";
    document.body.appendChild(host);
    // `./tabs.js` was already imported by an earlier case in this file, so the
    // doMock above cannot take: a mocked module is evaluated once and cached.
    // The real module's store subscriber renders into `#tab-list`, and `$` throws
    // on a missing element, so a late render would land in the run as an
    // unhandled error with no test to attach it to.
    const tabList = document.createElement("div");
    tabList.id = "tab-list";
    document.body.appendChild(tabList);

    // The page's own loader, not `showHistoryView`: that one TOGGLES the tab, which
    // is a round trip and paints nothing. `loadHistoryView` is what every door
    // reaches through the tab factory's lazy import.
    const { loadHistoryView } = await import("./history.js");
    loadHistoryView();
    await vi.waitFor(() => {
      if (host.querySelector("[data-key]") === null) {
        throw new Error("not rendered");
      }
    });

    const row = host.querySelector<HTMLElement>('[data-key="r:wf_a11y"]')!;
    // The row is a plain container; its two controls are siblings.
    expect(row.getAttribute("role")).toBeNull();
    expect(row.getAttribute("tabindex")).toBeNull();
    const open = row.querySelector<HTMLElement>("button.list-row-name")!;
    // The name still opens with the action, then states the verdict.
    expect(open.getAttribute("aria-label")).toBe("Open nightly-sweep, failed");
    // The glyph carrying it is decorative: the name already says the word.
    expect(row.querySelector(".tool-outcome-badge")?.getAttribute("aria-hidden")).toBe("true");
    expect(row.textContent).not.toContain("failed");

    document.body.removeChild(host);
    for (const m of [
      "./actions/chat.js",
      "./actions/chat-search.js",
      "./actions/index.js",
      "./chat.js",
      "./run-view.js",
      "./tabs.js",
      "@cplieger/ui-primitives/skeleton",
      "./editor-openers.js",
      "./navigate.js",
      "./scroll.js",
      "./tool-group.js",
    ]) {
      vi.doUnmock(m);
    }
  });
});
