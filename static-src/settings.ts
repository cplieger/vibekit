// ---------------------------------------------------------------------------
// Settings panel UI. Global preferences (last_model, notifications)
// live in server-side /api/settings; per-device state lives in
// ui-state.ts.
// ---------------------------------------------------------------------------

import { initAllModals } from "./modals.js";
import { toggleSettingsView, toggleGitView } from "./tabs.js";
import { initGitPanel, loadGitRepos } from "./git.js";
import { getGitTab } from "./git-tabs.js";
import { restoreFileBrowser } from "./files.js";
import { restoreEditorTabs } from "./editor-core.js";
import { restoreShell } from "./shell.js";
import { initTools, loadToolsList } from "./tools.js";
import { restoreNotifications } from "./notify.js";
import { loadSettings, patchSettings, initSettingsTracking } from "./persist.js";
import type { AppSettings } from "./persist.js";
import * as uiState from "./ui-state.js";
import { initThemeToggle } from "./theme.js";
import { initSettingsTabs } from "./settings-tabs.js";
import { initPermissionsUI, initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { initMCP } from "./mcp-ui.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";
import { initHooks, loadHooks } from "./hooks.js";
import { initKiroConfig, loadKiroConfig } from "./kiro-config.js";
// (forge-auth.ts is imported by git-sources-tab.ts now; no settings-side
// import needed since the "Git & forges" Settings tab was retired.)
import { apiGet } from "./api-client.js";
import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import { initNotificationToggles } from "./settings-notifications.js";

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { saveSteering, logout, setKiroSetting } from "./actions/settings.js";
import { runDiagnostics } from "./actions/tools.js";
import {
  bindLoadingState,
  registerCleanup,
  debouncedDispatch,
  subscribeByName,
} from "./actions/index.js";

// Shared generation counter for kiro-setting saves. Last-write-wins:
// if two settings change in rapid succession, only the final save
// updates the indicator. This is acceptable because the indicator is
// purely cosmetic (each save is independent on the server).
let kiroSettingGen = 0;

/**
 * Encapsulates the gen-counter + showSaving/showSaved/showError lifecycle
 * for dispatching a kiro-cli setting change. Deduplicates the identical
 * pattern used in initExperimentalToggles and initCompactionSettings.
 */
function dispatchKiroSetting(
  key: string,
  value: string,
  input: HTMLInputElement,
  previousValue?: string,
): void {
  showSaving();
  const gen = ++kiroSettingGen;
  void setKiroSetting
    .dispatch(
      { key, value, input, ...(previousValue !== undefined ? { previousValue } : {}) },
      { silent: true },
    )
    .then((r) => {
      if (gen !== kiroSettingGen) {
        return;
      }
      if (r === null) {
        showError();
      } else {
        showSaved();
      }
    });
}

export type { AppSettings } from "./persist.js";
export { loadSettings } from "./persist.js";

/** Fetch settings from server and apply notification state only.
 *  Used for lightweight re-sync (e.g. after login) without touching
 *  per-device UI state. Compare with restoreAll() which also restores
 *  localStorage-based UI (shell, file browser, editor tabs). */
export async function syncSettings(): Promise<AppSettings> {
  const s = await loadSettings();
  // Seed the dedup tracker BEFORE any code path can fire patchSettings().
  // The bootstrap subscription fires (e.g. repo-picker.onSelectionChange)
  // would otherwise re-PATCH /api/settings with values it just loaded
  // from /api/settings, triggering the "Saving..." animation on every
  // page reload.
  initSettingsTracking(s);
  restoreNotifications(s);
  return s;
}

/** Restore all state: per-device UI from localStorage, global prefs from
 *  the loaded settings payload. Called once at startup. Unlike syncSettings(),
 *  this also restores shell, file browser, and editor tabs. (Theme is applied
 *  separately by initThemeToggle() during initUI.) */
export function restoreAll(s: AppSettings): void {
  const ui = uiState.load();
  if (ui.shell_open) {
    restoreShell();
  }
  if (ui.fb_path !== "") {
    restoreFileBrowser(ui.fb_path);
  }
  if (ui.editor_files.length > 0) {
    restoreEditorTabs(ui.editor_files);
  }

  restoreNotifications(s);
  // Theme is applied by initThemeToggle() (initUI), which constructs the
  // createTheme controller — it reads the ui-state blob and applies the
  // resolved theme on construction. No separate apply is needed here.
  initPermissionsUI(s);
  initNativePolicyUI();
  initDebugLogsToggle(s);
  initChatRetention(s);
}

// --- Chat retention (vibekit-owned; /api/settings chat_retention_days) ---
//
// kiro-cli's cleanup.periodDays is pinned to 0/never — vibekit owns retention
// end to end. The Days-kept number field carries 0 (off) .. N (keep N days);
// the Keep-forever checkbox overrides it to -1 (archive, never purged) and
// disables the number field. Writes go to /api/settings; the settings_updated
// SSE refreshes retention.ts (archive-vs-delete-on-close + History visibility).
function initChatRetention(s: AppSettings): void {
  const daysInput = document.getElementById("chat-retention-days") as HTMLInputElement | null;
  const foreverInput = document.getElementById("chat-retention-forever") as HTMLInputElement | null;
  if (daysInput === null || foreverInput === null) {
    return;
  }
  const current = typeof s.chat_retention_days === "number" ? s.chat_retention_days : 1;
  foreverInput.checked = current === -1;
  daysInput.disabled = current === -1;
  if (current >= 0) {
    daysInput.value = String(current);
  }

  const persist = (value: number, input: HTMLInputElement): void => {
    void patchSettings({ chat_retention_days: value }, input);
  };

  daysInput.addEventListener("change", () => {
    if (foreverInput.checked) {
      return; // forever wins; the number field is disabled
    }
    const n = parseInt(daysInput.value, 10);
    persist(!isNaN(n) && n >= 0 ? n : 0, daysInput);
  });
  foreverInput.addEventListener("change", () => {
    daysInput.disabled = foreverInput.checked;
    if (foreverInput.checked) {
      persist(-1, foreverInput);
      return;
    }
    const n = parseInt(daysInput.value, 10);
    persist(!isNaN(n) && n >= 0 ? n : 1, foreverInput);
  });
}

// --- UI init ---

/** Load the lists the Instructions tab shows: the .kiro/ steering-docs/
 *  skills/agents list, the workspace knowledge bases, and the hooks
 *  dashboard (the "workspace context" family). Fired once on the tab's
 *  first activation via the settings-tabs loader map. */
function loadInstructionsPanel(): void {
  loadKiroConfig();
  loadKnowledge();
  loadHooks();
}

export function initUI(): void {
  initThemeToggle();

  // Settings gear opens the tabbed Settings panel. Default tab is General;
  // deep-link URLs (e.g. /settings/tools) override this via applyRoute.
  // Panel data loads lazily on each tab's first activation (see the loader
  // map below) — the gear no longer preloads the Tools list while opening
  // the General panel (B9).
  $.settingsBtn.addEventListener("click", () => {
    toggleSettingsView("general");
  });

  // Per-tab lazy data loaders: fired by settings-tabs on the first
  // activation of each tab. General has no loader (static panel).
  initSettingsTabs({
    tools: loadToolsList,
    permissions: loadNativePolicy,
    instructions: loadInstructionsPanel,
  });
  initSteeringEditor();
  initLogoutButton();
  initNotificationToggles();
  initDiagnostics();
  initExperimentalToggles();

  initTools();
  initMCP();
  initKnowledge();
  initHooks();
  initKiroConfig();
  initAllModals();

  // The "Git & forges" tab in Settings was retired with the multi-repo
  // git-page rewrite — forge accounts now live on the Sources tab of
  // the git view. So there's no longer a tab-change → load mapping
  // here. (forge-auth.ts is still imported because it powers the
  // accounts UI inside that Sources tab.)

  $.gitBtn.addEventListener("click", () => {
    // Open to whichever sub-tab is currently active (defaults to "changes"
    // on first open) so the URL the tab pushes matches the visible panel.
    toggleGitView(getGitTab(), loadGitRepos);
  });
}

/** Post-auth UI init: fetches that must not fire on the login screen (B2).
 *  loadAbout hits /api/version; initGitPanel starts the git-badge poll
 *  (/api/git/status-all + /api/forges every 15s). Called once by app.ts
 *  after whoami succeeds — at boot when already authenticated, or after
 *  the first successful login. */
export function initPostAuthUI(): void {
  void loadAbout();
  initGitPanel();
}

// --- Steering (auto-save with debounce) ---

function initSteeringEditor(): void {
  const textarea = $.steeringInput;
  // debouncedDispatch coalesces rapid keystrokes into a single trailing
  // dispatch after the quiet window (replaces the manual clearTimeout +
  // setTimeout(600) + saveGen pattern). saveGen is no longer needed: the
  // action has scope:"settings", so dispatches serialize (ordered
  // resolution), and the indicator is driven by the action's own
  // lifecycle events below rather than a per-dispatch .then().
  const debouncedSave = debouncedDispatch(saveSteering, { wait: 600 });

  void apiGet<{ content?: string }>("/api/steering").then((d) => {
    if (d?.content !== undefined) {
      textarea.value = d.content;
    }
  });

  const unsub = subscribeByName("settings.save_steering", (inst) => {
    if (inst.status === "success") {
      showSaved();
    } else if (inst.status === "error") {
      showError();
    }
  });

  textarea.addEventListener("input", () => {
    showSaving();
    debouncedSave({ content: textarea.value });
  });

  registerCleanup(() => {
    // Stop touching the indicator (mirrors the original cleanup, which
    // flushed without updating it), then flush any pending edit so an
    // unsaved change still persists on teardown.
    unsub();
    if (debouncedSave.isPending()) {
      void debouncedSave.flush({ content: textarea.value });
    }
  });
}

// --- Logout ---

function initLogoutButton(): void {
  bindLoadingState("settings.logout", $.logoutBtn);
  $.logoutBtn.addEventListener("click", () => {
    void logout.dispatch({ emailEl: $.userEmail, stAuthEl: $.stAuth });
  });
}

// --- About / Diagnostics (Settings → General) ---

interface VersionPayload {
  vibekit?: string;
  kiro_cli?: string;
}

/** Fetches build versions once at Settings init and renders the About
 *  grid. Both values are passive — they don't change until container
 *  restart — so we do not refresh after initial load. */
async function loadAbout(): Promise<void> {
  const v = await apiGet<VersionPayload>("/api/version");
  const vibekitEl = document.getElementById("about-vibekit");
  const kiroEl = document.getElementById("about-kirocli");
  if (vibekitEl !== null) {
    vibekitEl.textContent = v?.vibekit ?? "—";
  }
  if (kiroEl !== null) {
    kiroEl.textContent = v?.kiro_cli ?? "—";
  }
}

/** Pull a kiro-cli / KAS version out of a diagnostics report so the About
 *  panel can surface it as a dedicated row. The report is the raw
 *  `kiro-cli diagnostic --format json-pretty` output; the version lives under
 *  `q-details.version` in the Amazon-Q-derived schema, with a few fallbacks for
 *  forks that name it differently (or a server that folds it in top-level).
 *  Returns "" when the report isn't JSON or carries no recognisable version, in
 *  which case the caller omits the row. */
export function extractDiagnosticVersion(report: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(report);
  } catch {
    return "";
  }
  const paths: readonly string[][] = [
    ["q-details", "version"],
    ["kiro_cli", "version"],
    ["kiro-cli", "version"],
    ["cli", "version"],
    ["version"],
    ["kiro_cli"],
    ["kas"],
  ];
  for (const path of paths) {
    const v = digPath(parsed, path);
    if (typeof v === "string" && v.trim() !== "") {
      return v.trim();
    }
  }
  return "";
}

/** Walk `obj` down `path`, returning undefined at the first non-object hop. */
function digPath(obj: unknown, path: readonly string[]): unknown {
  let cur: unknown = obj;
  for (const key of path) {
    if (typeof cur !== "object" || cur === null) {
      return undefined;
    }
    cur = (cur as Record<string, unknown>)[key];
  }
  return cur;
}

/** Copy `text` to the clipboard, resolving to whether it worked. The clipboard
 *  API rejects on a non-secure-context self-host (plain-http LAN IP) or in an
 *  iframe, so the caller falls back to the always-present textarea. */
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** Wires the "Run diagnostics" button. Shows a spinner (keeping the label)
 *  while kiro-cli collects its report, then renders the FULL report into a
 *  readonly, selectable textarea with an explicit Copy button — the report can
 *  be large and the clipboard is unreachable on non-HTTPS self-hosts, so a
 *  truncated ephemeral string is never the only surface. A kiro-cli version row
 *  is shown when the payload carries one. Failures surface as an error status
 *  so the user can re-run. */
export function initDiagnostics(): void {
  const btn = document.getElementById("diagnostics-run") as HTMLButtonElement | null;
  const status = document.getElementById("diagnostics-status") as HTMLParagraphElement | null;
  if (btn === null || status === null) {
    return;
  }

  // Announce the transient status transitions (collecting / ready / error) to
  // assistive tech. Setting the live-region role here keeps announcements
  // working regardless of the static markup (see the index.html note in the
  // task report).
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");

  // Build the copyable result surface once, right under the status line.
  const host = status.parentElement ?? btn.parentElement;
  const versionRow = el("p", {
    className: "section-hint diagnostics-version",
  }) as HTMLParagraphElement;
  versionRow.hidden = true;

  const result = el("textarea", {
    className: "diagnostics-result",
    "aria-label": "Diagnostics report",
    spellcheck: "false",
  }) as HTMLTextAreaElement;
  result.readOnly = true;
  result.hidden = true;

  const copyBtn = el(
    "button",
    { type: "button", className: "btn-small diagnostics-copy" },
    "Copy report",
  ) as HTMLButtonElement;
  const copyWrap = el(
    "div",
    { className: "diagnostics-result-actions" },
    copyBtn,
  ) as HTMLDivElement;
  copyWrap.hidden = true;

  host?.append(versionRow, copyWrap, result);

  copyBtn.addEventListener("click", () => {
    void copyToClipboard(result.value).then((ok) => {
      if (ok) {
        status.hidden = false;
        status.textContent = "Copied report to clipboard.";
      } else {
        // Select the text so the keyboard shortcut copies the whole report.
        result.focus();
        result.select();
        status.hidden = false;
        status.textContent = "Press Ctrl/Cmd+C to copy the selected report.";
      }
    });
  });

  bindLoadingState("tools.run_diagnostics", btn, { pendingClass: "btn-loading" });

  // eslint-disable-next-line @typescript-eslint/no-misused-promises
  btn.addEventListener("click", async () => {
    status.hidden = false;
    status.textContent = "Collecting diagnostics\u2026";
    result.hidden = true;
    copyWrap.hidden = true;
    versionRow.hidden = true;
    const out = await runDiagnostics.dispatch(undefined);
    if (out === null || out.error !== undefined) {
      status.textContent = out?.error ?? "Diagnostics failed. Check server logs.";
      return;
    }
    const report = out.report ?? "";
    // Full report in a selectable, newline-preserving textarea (the durable
    // surface), plus a version row when the payload carries one.
    result.value = report;
    result.hidden = false;
    copyWrap.hidden = false;
    const version = extractDiagnosticVersion(report);
    if (version !== "") {
      versionRow.textContent = `kiro-cli ${version}`;
      versionRow.hidden = false;
    }
    // Clipboard copy is a convenience; the textarea above works regardless.
    const copied = await copyToClipboard(report);
    status.textContent = copied
      ? `Report ready — copied ${report.length.toLocaleString()} characters to your clipboard.`
      : "Report ready — copy it from the box below.";
  });
}

// (loadIdentity was removed: it duplicated the /api/whoami fetch that
// checkAuthAndStart / onLoginSuccess in app.ts already perform — both call
// setUserEmail with their result, so the sidebar label needs no second
// fetch at boot.)

// --- User display ---

export function setUserEmail(email: string): void {
  $.userEmail.textContent = email;
  $.stAuth.textContent = email !== "" ? "signed in" : "not signed in";
}

// --- Experimental flag toggles (Settings → General) ---
//
// kiro-cli experimental features gated by settings keys (see the
// experimentalFlags registry below for the full set). Vibekit seeds them at
// container boot (entrypoint.sh); this UI lets the user flip each one.

interface KiroSettingPayload {
  key?: string;
  value?: string;
}

// experimentalFlags is the single source of truth for which kiro-cli
// flags we expose in the UI. Adding a row here creates the toggle,
// its description, and the get/put wiring automatically.
const experimentalFlags: readonly {
  key: string;
  inputID: string;
  inverted?: boolean;
}[] = [
  { key: "chat.enableCheckpoint", inputID: "flag-checkpoint" },
  { key: "chat.enableTodoList", inputID: "flag-todolist" },
  { key: "chat.enableKnowledge", inputID: "flag-knowledge" },
  { key: "hooks.showStatus", inputID: "flag-hooks-status" },
  { key: "telemetry.enabled", inputID: "flag-telemetry" },
  { key: "toolSearch.enabled", inputID: "flag-tool-search" },
  // Checked = disable inheritance of default steering/skills/AGENTS.md by
  // custom agents (kiro-cli 2.10+). Not inverted: on = true = disabled.
  // Seeded false in entrypoint so the unset->on fallback doesn't mis-render.
  { key: "chat.disableInheritingDefaultResources", inputID: "flag-disable-inherit-resources" },
];

function initExperimentalToggles(): void {
  const inputs = experimentalFlags.map(
    (flag) => document.getElementById(flag.inputID) as HTMLInputElement | null,
  );
  void Promise.all(
    experimentalFlags.map((flag) =>
      apiGet<KiroSettingPayload>(`/api/kiro-settings?key=${encodeURIComponent(flag.key)}`),
    ),
  ).then((results) => {
    for (let i = 0; i < experimentalFlags.length; i++) {
      const input = inputs[i] ?? null;
      if (input === null) {
        continue;
      }
      const v = results[i]?.value ?? "";
      const isOn = v === "" || v === "true";
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      input.checked = experimentalFlags[i]!.inverted ? !isOn : isOn;
    }
  });
  for (let i = 0; i < experimentalFlags.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const flag = experimentalFlags[i]!;
    const input = inputs[i] ?? null;
    if (input === null) {
      continue;
    }
    input.addEventListener("change", () => {
      const wireValue = flag.inverted
        ? input.checked
          ? "false"
          : "true"
        : input.checked
          ? "true"
          : "false";
      dispatchKiroSetting(flag.key, wireValue, input);
    });
  }
  initCompactionSettings();
}

// --- Compaction settings ---

const compactionSettings: readonly {
  key: string;
  inputID: string;
  isBool: boolean;
}[] = [
  { key: "chat.disableAutoCompaction", inputID: "flag-auto-compact", isBool: true },
  { key: "compaction.excludeMessages", inputID: "compact-keep-messages", isBool: false },
  {
    key: "compaction.excludeContextWindowPercent",
    inputID: "compact-context-buffer",
    isBool: false,
  },
];

function initCompactionSettings(): void {
  const inputs = compactionSettings.map(
    (s) => document.getElementById(s.inputID) as HTMLInputElement | null,
  );
  // Snapshot values on focus so we can pass the true previous value
  // to the action (before the change event updates the input).
  const snapshots = new Map<HTMLInputElement, string>();
  for (let i = 0; i < compactionSettings.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const s = compactionSettings[i]!;
    const input = inputs[i] ?? null;
    if (input === null || s.isBool) {
      continue;
    }
    input.addEventListener("focus", () => {
      snapshots.set(input, input.value);
    });
  }
  void Promise.all(
    compactionSettings.map((s) =>
      apiGet<KiroSettingPayload>(`/api/kiro-settings?key=${encodeURIComponent(s.key)}`),
    ),
  ).then((results) => {
    for (let i = 0; i < compactionSettings.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const s = compactionSettings[i]!;
      const input = inputs[i] ?? null;
      if (input === null) {
        continue;
      }
      const v = results[i]?.value ?? "";
      if (s.isBool) {
        input.checked = v !== "true";
      } else {
        if (v !== "") {
          input.value = v;
        }
      }
    }
  });
  for (let i = 0; i < compactionSettings.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const s = compactionSettings[i]!;
    const input = inputs[i] ?? null;
    if (input === null) {
      continue;
    }
    input.addEventListener("change", () => {
      let value: string;
      if (s.isBool) {
        value = input.checked ? "false" : "true";
      } else {
        value = input.value;
      }
      const previousValue = s.isBool ? undefined : snapshots.get(input);
      if (!s.isBool) {
        snapshots.set(input, input.value);
      }
      dispatchKiroSetting(s.key, value, input, previousValue);
    });
  }
}

// --- Default agent picker (Settings → Custom instructions) ---
//
// REMOVED: a Settings-level *default*-agent picker. Role selection now
// lives on the prompt-bar role pill (role-picker.ts, #role-pill): it picks
// the agent per chat (built-in or a workspace custom agent from
// .kiro/agents/), which fits vibekit's per-chat model better than a
// persistent default. To set a container-wide default agent instead, use
// `docker exec vibekit kiro-cli agent set-default <name>`.

// --- Debug logs toggle ---
//
// Separate from the kiro-cli experimental flags: this flips vibekit's
// own slog level via /api/settings rather than the kiro-cli settings
// endpoint. When on, server-side logs include slog.Debug entries;
// read them with `docker logs vibekit`.

function initDebugLogsToggle(initial: AppSettings): void {
  const input = document.getElementById("flag-debug-logs") as HTMLInputElement | null;
  if (input === null) {
    return;
  }
  input.checked = initial.debug_logs === true;
  input.addEventListener("change", () => {
    void patchSettings({ debug_logs: input.checked }, input);
  });
}
