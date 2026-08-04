// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for settings.ts's diagnostics surface: the pure version extractor and
// the initDiagnostics DOM flow (copyable textarea + version row + Copy button +
// clipboard fallback). settings.ts's many feature-module imports are stubbed so
// the module loads in isolation; only runDiagnostics + bindLoadingState are
// given behaviour.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const H = vi.hoisted(() => ({
  mockRun: vi.fn(),
  mockBind: vi.fn(),
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
vi.mock("./actions/settings.js", () => ({ saveSteering: {}, logout: {}, setKiroSetting: {} }));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./wire/decoders.gen.js", () => ({ decodeWhoamiResponse: vi.fn() }));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("./persist.js", () => ({
  loadSettings: vi.fn(),
  patchSettings: vi.fn(),
  initSettingsTracking: vi.fn(),
}));
// Feature modules settings.ts wires in initUI — inert stubs so the import graph
// loads without side effects (initUI is never called here).
vi.mock("./modals.js", () => ({}));
vi.mock("./tabs.js", () => ({}));
vi.mock("./git.js", () => ({}));
vi.mock("./git-tabs.js", () => ({}));
vi.mock("./files.js", () => ({}));
vi.mock("./editor-core.js", () => ({}));
vi.mock("./shell.js", () => ({}));
vi.mock("./tools.js", () => ({}));
vi.mock("./notify.js", () => ({}));
vi.mock("./ui-state.js", () => ({}));
vi.mock("./theme.js", () => ({}));
vi.mock("./settings-tabs.js", () => ({}));
vi.mock("./permissions-ui.js", () => ({}));
vi.mock("./mcp-ui.js", () => ({}));
vi.mock("./knowledge.js", () => ({}));
vi.mock("./hooks.js", () => ({}));
vi.mock("./settings-notifications.js", () => ({}));

const { extractDiagnosticVersion, initDiagnostics } = await import("./settings.js");

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
