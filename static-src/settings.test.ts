// ---------------------------------------------------------------------------
// Tests for settings.ts's diagnostics surface: the pure version extractor and
// the initDiagnostics DOM flow (copyable textarea + version row + Copy button +
// clipboard fallback), plus the chat-retention row's Keep-forever reveal.
// settings.ts's many feature-module imports are stubbed so the module loads in
// isolation; only runDiagnostics + bindLoadingState are given behaviour.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const H = vi.hoisted(() => ({
  mockRun: vi.fn(),
  mockBind: vi.fn(),
  mockApplyTheme: vi.fn(),
  mockKiroDispatch: vi.fn(),
}));

vi.mock("./actions/tools.js", () => ({
  runDiagnostics: { dispatch: (...a: unknown[]) => H.mockRun(...a) },
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: (...a: unknown[]) => H.mockBind(...a),
  registerCleanup: vi.fn(),
  debouncedDispatch: vi.fn(() =>
    Object.assign(() => undefined, { isPending: () => false, flush: vi.fn() }),
  ),
  subscribeByName: vi.fn(() => () => undefined),
}));
vi.mock("./actions/settings.js", () => ({
  saveSteering: {},
  logout: {},
  setKiroSetting: { dispatch: (...a: unknown[]) => H.mockKiroDispatch(...a) },
}));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./wire/decoders.gen.js", () => ({ decodeWhoamiResponse: vi.fn() }));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
  STEERING_SAVE_KEY: "steering",
}));
vi.mock("./persist.js", () => ({
  loadSettings: vi.fn(),
  patchSettings: vi.fn(),
  initSettingsTracking: vi.fn(),
}));
// Feature modules settings.ts wires in initUI — inert stubs so the import graph
// loads without side effects (initUI is never called here).
vi.mock("./modals.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initAllModals: undefined,
}));
vi.mock("./tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  toggleSettingsView: undefined,
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  toggleGitView: undefined,
}));
vi.mock("./git.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initGitPanel: undefined,
  loadGitRepos: undefined,
}));
vi.mock("./git-tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  getGitTab: undefined,
}));
vi.mock("./files.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  restoreFileBrowser: undefined,
}));
vi.mock("./editor-core.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  restoreEditorTabs: undefined,
}));
vi.mock("./shell.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  restoreShell: undefined,
}));
vi.mock("./tools.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  loadToolsList: undefined,
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initTools: undefined,
}));
vi.mock("./notify.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  restoreNotifications: undefined,
}));
vi.mock("./theme.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initThemeToggle: undefined,
  applyThemeChoice: H.mockApplyTheme,
}));
vi.mock("./settings-tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initSettingsTabs: undefined,
}));
vi.mock("./permissions-ui.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  loadNativePolicy: undefined,
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initNativePolicyUI: undefined,
  initPermissionsUI: undefined,
}));
vi.mock("./mcp-ui.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initMCP: undefined,
}));
vi.mock("./knowledge.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initKnowledge: undefined,
  loadKnowledge: undefined,
}));
vi.mock("./settings-notifications.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  initNotificationToggles: undefined,
}));

const {
  adoptThemeFromSettings,
  extractDiagnosticVersion,
  initChatRetention,
  initDiagnostics,
  initExperimentalToggles,
  themeStorage,
  _resetThemeForTest,
} = await import("./settings.js");

async function flush(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

function seedDom(): void {
  document.body.innerHTML = `
    <div class="section-option">
      <button id="diagnostics-run" class="btn">Run diagnostics</button>
      <p id="diagnostics-status" class="section-hint" hidden></p>
    </div>`;
}

function stubClipboard(writeText: () => Promise<void>): void {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
}

function clickRun(): void {
  document.getElementById("diagnostics-run")?.dispatchEvent(new MouseEvent("click"));
}

beforeEach(() => {
  vi.clearAllMocks();
  seedDom();
});

describe("extractDiagnosticVersion", () => {
  it("reads q-details.version (the Amazon-Q-derived schema)", () => {
    expect(extractDiagnosticVersion('{"q-details":{"version":"2.12.0"}}')).toBe("2.12.0");
  });

  it("falls back to a top-level version field", () => {
    expect(extractDiagnosticVersion('{"version":"1.2.3"}')).toBe("1.2.3");
  });

  it("reads a top-level kiro_cli string", () => {
    expect(extractDiagnosticVersion('{"kiro_cli":"2.12.0"}')).toBe("2.12.0");
  });

  it("returns '' for a non-JSON report", () => {
    expect(extractDiagnosticVersion("signal: killed")).toBe("");
  });

  it("returns '' when no recognisable version is present", () => {
    expect(extractDiagnosticVersion('{"system":{"os":"linux"}}')).toBe("");
  });
});

describe("initDiagnostics", () => {
  it("renders the full report into a copyable textarea + version row", async () => {
    const report = '{"q-details":{"version":"2.12.0"}}';
    H.mockRun.mockResolvedValue({ report });
    const writeText = vi.fn(() => Promise.resolve());
    stubClipboard(writeText);

    initDiagnostics();
    clickRun();
    await flush();

    const result = document.querySelector<HTMLTextAreaElement>(".diagnostics-result");
    expect(result).not.toBeNull();
    expect(result?.value).toBe(report);
    expect(result?.hidden).toBe(false);
    expect(result?.readOnly).toBe(true);

    const version = document.querySelector<HTMLElement>(".diagnostics-version");
    expect(version?.hidden).toBe(false);
    expect(version?.textContent).toBe("kiro-cli 2.12.0");

    const status = document.getElementById("diagnostics-status");
    expect(status?.getAttribute("aria-live")).toBe("polite");
    expect(status?.textContent).toContain("Report ready");
    expect(writeText).toHaveBeenCalledWith(report);
  });

  it("keeps the report available when the clipboard is blocked", async () => {
    const report = "plain diagnostics text";
    H.mockRun.mockResolvedValue({ report });
    stubClipboard(() => Promise.reject(new Error("blocked")));

    initDiagnostics();
    clickRun();
    await flush();

    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.value).toBe(report);
    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.hidden).toBe(false);
    // No version row for a non-JSON report.
    expect(document.querySelector<HTMLElement>(".diagnostics-version")?.hidden).toBe(true);
    expect(document.getElementById("diagnostics-status")?.textContent).toContain(
      "copy it from the box below",
    );
  });

  it("surfaces an error status and hides the result on failure", async () => {
    H.mockRun.mockResolvedValue({ error: "diagnostic command failed" });
    stubClipboard(() => Promise.resolve());

    initDiagnostics();
    clickRun();
    await flush();

    expect(document.getElementById("diagnostics-status")?.textContent).toBe(
      "diagnostic command failed",
    );
    expect(document.querySelector<HTMLTextAreaElement>(".diagnostics-result")?.hidden).toBe(true);
  });

  it("copies the report via the Copy button", async () => {
    const report = "some report";
    H.mockRun.mockResolvedValue({ report });
    const writeText = vi.fn(() => Promise.resolve());
    stubClipboard(writeText);

    initDiagnostics();
    clickRun();
    await flush();
    writeText.mockClear();

    document
      .querySelector<HTMLButtonElement>(".diagnostics-copy")
      ?.dispatchEvent(new MouseEvent("click"));
    await flush();
    expect(writeText).toHaveBeenCalledWith(report);
    expect(document.getElementById("diagnostics-status")?.textContent).toBe(
      "Copied report to clipboard.",
    );
  });
});

// Keep forever means -1, which has no day count, so the Days-kept field is
// HIDDEN rather than disabled — a greyed-out field still showing the last
// number reads as the value in force. The number survives in the DOM, so
// unchecking restores it instead of falling back to a default.
describe("initChatRetention", () => {
  function seedRetentionDom(): void {
    document.body.innerHTML = `
      <div class="section-option">
        <label for="chat-retention-forever" class="section-option-label">Keep forever</label>
        <input type="checkbox" id="chat-retention-forever">
      </div>
      <div class="section-option" id="chat-retention-days-row">
        <label for="chat-retention-days" class="section-option-label">Days kept</label>
        <input type="number" id="chat-retention-days" min="0" max="90" value="1">
      </div>`;
  }

  function daysRow(): HTMLElement {
    return document.getElementById("chat-retention-days-row")!;
  }

  function daysInput(): HTMLInputElement {
    return document.getElementById("chat-retention-days") as HTMLInputElement;
  }

  function foreverInput(): HTMLInputElement {
    return document.getElementById("chat-retention-forever") as HTMLInputElement;
  }

  function toggleForever(checked: boolean): void {
    foreverInput().checked = checked;
    foreverInput().dispatchEvent(new Event("change"));
  }

  beforeEach(() => {
    seedRetentionDom();
  });

  it("shows the Days-kept row for a day count", () => {
    initChatRetention({ chat_retention_days: 14 });

    expect(foreverInput().checked).toBe(false);
    expect(daysRow().classList.contains("hidden")).toBe(false);
    expect(daysInput().value).toBe("14");
  });

  it("hides the Days-kept row when the stored value is forever", () => {
    initChatRetention({ chat_retention_days: -1 });

    expect(foreverInput().checked).toBe(true);
    expect(daysRow().classList.contains("hidden")).toBe(true);
    // Hidden, never disabled: a disabled field greys out the last number and
    // presents it as the value in force.
    expect(daysInput().disabled).toBe(false);
  });

  it("hides the row on check and restores it, with its number, on uncheck", async () => {
    const { patchSettings } = await import("./persist.js");
    initChatRetention({ chat_retention_days: 30 });

    toggleForever(true);
    expect(daysRow().classList.contains("hidden")).toBe(true);
    expect(patchSettings).toHaveBeenLastCalledWith({ chat_retention_days: -1 }, expect.anything());

    toggleForever(false);
    expect(daysRow().classList.contains("hidden")).toBe(false);
    expect(daysInput().value).toBe("30");
    expect(patchSettings).toHaveBeenLastCalledWith({ chat_retention_days: 30 }, expect.anything());
  });
});

// ---------------------------------------------------------------------------
// The theme's authority is a config.json key; the localStorage blob holds a
// pre-paint CACHE of it. This is the policy joining the two, and each case here
// is a way that policy can be wrong in a manner a reader would notice:
//
//   - a stale cache overwriting a deliberate change (a migration path pretending
//     to be a cache),
//   - the value not repainting when another device chose it,
//   - an adopted value going straight back out as a write, which on the
//     settings_updated path re-broadcasts settings_updated.
//
// The one-time carry-across is the single value the deletion of the old
// whole-document arrangement hands over, because the theme is the only loss a
// reader would SEE — on the very next load, as the wrong colour.
// ---------------------------------------------------------------------------

describe("the theme, between config.json and its paint cache", () => {
  beforeEach(async () => {
    localStorage.clear();
    _resetThemeForTest();
    // The real controller's set() calls back through the storage adapter, and
    // that call is the ONLY path to the write-back guard. A mock that merely
    // records the call would make every case below vacuous — measured: with the
    // guard deleted the suite stayed green until this line existed.
    H.mockApplyTheme.mockReset();
    H.mockApplyTheme.mockImplementation((choice: unknown) => {
      themeStorage.set(choice as string);
    });
    const { patchSettings } = await import("./persist.js");
    vi.mocked(patchSettings).mockClear();
  });

  it("adopts the server's theme and refreshes the cache", async () => {
    const { cachedTheme } = await import("./device-view.js");
    adoptThemeFromSettings({ theme: "light" });

    expect(cachedTheme()).toBe("light");
    expect(H.mockApplyTheme).toHaveBeenCalledWith("light");
  });

  it("does not write an adopted value back, which is what would loop", async () => {
    const { patchSettings } = await import("./persist.js");
    adoptThemeFromSettings({ theme: "dark" });
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("carries the cache across ONCE when the server has no theme", async () => {
    const { cacheTheme } = await import("./device-view.js");
    const { patchSettings } = await import("./persist.js");
    cacheTheme("light");

    adoptThemeFromSettings({});

    // Written through, so the value stops being cache-only and starts travelling
    // to every other device — which is exactly what it could not do before.
    expect(patchSettings).toHaveBeenCalledWith({ theme: "light" });
  });

  it("carries nothing across when the cache is empty too", async () => {
    const { patchSettings } = await import("./persist.js");
    adoptThemeFromSettings({});
    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("does not re-adopt the cache on a LATER payload with no theme", async () => {
    const { cacheTheme } = await import("./device-view.js");
    const { patchSettings } = await import("./persist.js");
    // The reader chose a theme, then cleared it. A second adoption of the cache
    // would bring the cleared value back, which is the difference between a cache
    // and a migration path.
    adoptThemeFromSettings({ theme: "dark" });
    vi.mocked(patchSettings).mockClear();
    cacheTheme("dark");

    adoptThemeFromSettings({});

    expect(patchSettings).not.toHaveBeenCalled();
  });

  it("repaints when another device's choice arrives, and only when it differs", () => {
    adoptThemeFromSettings({ theme: "dark" });
    expect(H.mockApplyTheme).toHaveBeenCalledTimes(1);

    // The writer's own echo. Repainting anyway would be a second theme transition
    // for a change this device made itself.
    adoptThemeFromSettings({ theme: "dark" });
    expect(H.mockApplyTheme).toHaveBeenCalledTimes(1);

    adoptThemeFromSettings({ theme: "light" });
    expect(H.mockApplyTheme).toHaveBeenLastCalledWith("light");
  });

  it("ignores a value that is not one of the three choices", async () => {
    const { cachedTheme } = await import("./device-view.js");
    adoptThemeFromSettings({ theme: "chartreuse" });
    expect(cachedTheme()).toBeNull();
    expect(H.mockApplyTheme).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The experimental flags each report against their OWN key.
//
// Every kiro-cli flag has its own indicator slot beside its own label, so a write
// names the key it wrote and the generation guard is per key. It used to be one
// counter for the whole endpoint, which was correct for a single page-wide
// indicator and wrong here in both directions: flipping a second flag while the
// first was in flight suppressed the first flag's report entirely, leaving its
// slot spinning with nothing left to answer it.
//
// These need a dispatch that stays OUTSTANDING while the next one is made, which
// is what the deferred mock below hands back.
// ---------------------------------------------------------------------------

describe("initExperimentalToggles", () => {
  /** Resolvers for the dispatches made so far, in order. */
  let answer: ((r: unknown) => void)[];

  function seedFlagsDom(): void {
    document.body.innerHTML = `
      <input type="checkbox" id="flag-hooks-status">
      <input type="checkbox" id="flag-telemetry">
      <input type="checkbox" id="flag-disable-inherit-resources">`;
  }

  function box(id: string): HTMLInputElement {
    return document.getElementById(id) as HTMLInputElement;
  }

  function toggle(id: string, checked: boolean): void {
    box(id).checked = checked;
    box(id).dispatchEvent(new Event("change"));
  }

  /** Wire the toggles and wait out the initial read, so a late arrival of it
   *  cannot land between a test's own toggle and its assertion. */
  async function initFlags(): Promise<void> {
    initExperimentalToggles();
    await vi.waitFor(() => {
      expect(box("flag-telemetry").checked).toBe(true);
    });
  }

  beforeEach(() => {
    answer = [];
    H.mockKiroDispatch.mockImplementation(
      () =>
        new Promise((resolve) => {
          answer.push(resolve);
        }),
    );
    seedFlagsDom();
  });

  it("spins the flag's own key when it is flipped", async () => {
    const { showSaving } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);

    expect(showSaving).toHaveBeenCalledExactlyOnceWith("telemetry.enabled");
  });

  it("reports each flag against its own key when an earlier write answers late", async () => {
    const { showSaved } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);
    toggle("flag-hooks-status", false);
    expect(H.mockKiroDispatch).toHaveBeenCalledTimes(2);

    // The first flag answers while the second is still outstanding. Under one
    // shared counter the second write had already claimed it, so this report was
    // dropped and the telemetry slot never settled.
    answer[0]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalledWith("telemetry.enabled");
    });

    answer[1]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalledWith("hooks.showStatus");
    });
    expect(showSaved).toHaveBeenCalledTimes(2);
  });

  it("does not report a flag a newer write of the same flag has overtaken", async () => {
    const { showSaved } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-telemetry", false);
    toggle("flag-telemetry", true);
    expect(H.mockKiroDispatch).toHaveBeenCalledTimes(2);

    // Both answer; only the newer one owns the slot, so a broken guard shows up
    // as a second call rather than as a missing one.
    answer[0]?.({});
    answer[1]?.({});
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalled();
    });
    expect(showSaved).toHaveBeenCalledExactlyOnceWith("telemetry.enabled");
  });

  it("names the flag's own key when its write fails", async () => {
    const { showError } = await import("./save-indicator.js");
    await initFlags();

    toggle("flag-disable-inherit-resources", true);
    answer[0]?.(null);

    await vi.waitFor(() => {
      expect(showError).toHaveBeenCalledExactlyOnceWith("chat.disableInheritingDefaultResources");
    });
  });
});
