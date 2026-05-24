// ---------------------------------------------------------------------------
// Settings panel UI. Global preferences (last_model, notifications,
// auto_update) live in server-side /api/settings; per-device state lives in
// ui-state.ts.
// ---------------------------------------------------------------------------

import { initAllModals } from "./modals.js";
import type { WhoamiResponse } from "./wire/types.gen.js";
import { toggleSettingsView, toggleGitView } from "./tabs.js";
import { initGitPanel, loadGitRepos } from "./git.js";
import { restoreFileBrowser } from "./files.js";
import { restoreEditorTabs } from "./editor-core.js";
import { restoreShell } from "./shell.js";
import { initTools, loadToolsList } from "./tools.js";
import {
  restoreNotifications,
} from "./notify.js";
import { loadSettings, patchSettings } from "./persist.js";
import type { AppSettings } from "./persist.js";
import * as uiState from "./ui-state.js";
import { applyTheme, getSystemTheme, initThemeToggle } from "./theme.js";
import { initSettingsTabs } from "./settings-tabs.js";
import { initPermissionsUI, initShellPolicyUI } from "./permissions-ui.js";
import { initMCP } from "./mcp-ui.js";
// (forge-auth.ts is imported by git-sources-tab.ts now; no settings-side
// import needed since the "Git & forges" Settings tab was retired.)
import { apiGet, apiPost } from "./api-client.js";
import { $ } from "./dom.js";
import { initNotificationToggles } from "./settings-notifications.js";

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { saveSteeringAction, logoutAction, setKiroSettingAction } from "./actions/settings.js";

export type { AppSettings } from "./persist.js";
export { loadSettings } from "./persist.js";

export async function syncSettings(): Promise<AppSettings> {
  const s = await loadSettings();
  restoreNotifications(s);
  return s;
}

/** Restore all state: per-device UI from localStorage, global prefs from
 *  the loaded settings payload. Called once at startup. */
export function restoreAll(s: AppSettings): void {
  const ui = uiState.load();
  if (ui.shell_open) restoreShell();
  if (ui.fb_path !== "") restoreFileBrowser(ui.fb_path);
  if (ui.editor_files.length > 0) restoreEditorTabs(ui.editor_files);

  const autoUpdateToggle = $.autoUpdateToggle;
  autoUpdateToggle.checked = s.auto_update !== false;

  restoreNotifications(s);
  applyTheme(ui.theme ?? getSystemTheme());
  initPermissionsUI(s);
  initShellPolicyUI(s);
  initDebugLogsToggle(s);
}

// --- UI init ---

export function initUI(): void {
  initThemeToggle();

  // Settings gear opens the tabbed Settings panel. Default tab is General;
  // deep-link URLs (e.g. /settings/tools) override this via applyRoute.
  $.settingsBtn.addEventListener("click", () =>
    toggleSettingsView("general", loadToolsList));

  initSettingsTabs();
  initSteeringEditor();
  initLogoutButton();
  initNotificationToggles();
  initDiagnostics();
  initExperimentalToggles();
  void loadAbout();
  void loadIdentity();

  initTools();
  initMCP();
  initAllModals();

  // The "Git & forges" tab in Settings was retired with the multi-repo
  // git-page rewrite — forge accounts now live on the Sources tab of
  // the git view. So there's no longer a tab-change → load mapping
  // here. (forge-auth.ts is still imported because it powers the
  // accounts UI inside that Sources tab.)

  $.gitBtn.addEventListener("click", () =>
    toggleGitView(loadGitRepos));
  initGitPanel();
}

// --- Steering (auto-save with debounce) ---

function initSteeringEditor(): void {
  const textarea = $.steeringInput;
  let timer: ReturnType<typeof setTimeout> | undefined;

  void apiGet<{ content?: string }>("/api/steering").then((d) => {
    if (d?.content !== undefined) textarea.value = d.content;
  });

  textarea.addEventListener("input", () => {
    clearTimeout(timer);
    showSaving();
    timer = setTimeout(() => {
      void saveSteeringAction.dispatch({ content: textarea.value }, { silent: true })
        .then((r) => { if (r === null) showError(); else showSaved(); });
    }, 600);
  });
}

// --- Logout ---

function initLogoutButton(): void {
  $.logoutBtn.addEventListener("click", () => {
    void logoutAction.dispatch({ emailEl: $.userEmail, stAuthEl: $.stAuth });
  });
}

// --- About / Diagnostics (Settings → General) ---

interface VersionPayload { vibekit?: string; kiro_cli?: string }

/** Fetches build versions once at Settings init and renders the About
 *  grid. Both values are passive — they don't change until container
 *  restart — so we do not refresh after initial load. */
async function loadAbout(): Promise<void> {
  const v = await apiGet<VersionPayload>("/api/version");
  const vibekitEl = document.getElementById("about-vibekit");
  const kiroEl = document.getElementById("about-kirocli");
  if (vibekitEl !== null) vibekitEl.textContent = v?.vibekit ?? "—";
  if (kiroEl !== null) kiroEl.textContent = v?.kiro_cli ?? "—";
}

/** Wires the "Run diagnostics" button. Shows a spinner while kiro-cli
 *  collects its report, then copies the output to the clipboard and
 *  prints a summary below the button. Failures are surfaced as an
 *  error status so the user can manually re-run. */
function initDiagnostics(): void {
  const btn = document.getElementById("diagnostics-run") as HTMLButtonElement | null;
  const status = document.getElementById("diagnostics-status") as HTMLParagraphElement | null;
  if (btn === null || status === null) return;

  btn.addEventListener("click", async () => {
    btn.disabled = true;
    btn.classList.add("btn-loading");
    status.hidden = false;
    status.textContent = "Collecting diagnostics\u2026";
    const out = await apiPost<{ report?: string; error?: string }>("/api/diagnostics", {});
    btn.disabled = false;
    btn.classList.remove("btn-loading");
    if (out === null || out.error !== undefined) {
      status.textContent = out?.error ?? "Diagnostics failed. Check server logs.";
      return;
    }
    const report = out.report ?? "";
    try {
      await navigator.clipboard.writeText(report);
      status.textContent = `Copied ${report.length.toLocaleString()} chars to clipboard.`;
    } catch {
      // Clipboard may be unavailable (iframe, HTTPS-only policy). Fall
      // back to showing the first 400 chars in-place so the user can
      // copy manually.
      status.textContent = report.slice(0, 400) + (report.length > 400 ? "…" : "");
    }
  });
}

/** Refreshes the identity label from the live /api/whoami endpoint.
 *  Called on startup to populate the sidebar email. */
async function loadIdentity(): Promise<void> {
  const info = await apiGet<WhoamiResponse>("/api/whoami");
  if (info?.email !== undefined && info.email !== "") setUserEmail(info.email);
}

// --- User display ---

export function setUserEmail(email: string): void {
  $.userEmail.textContent = email;
  $.stAuth.textContent = email !== "" ? "signed in" : "not signed in";
}

// --- Experimental flag toggles (Settings → General) ---
//
// Three kiro-cli experimental features gated by settings keys. Vibekit
// enables all three by default at container boot (entrypoint.sh); this
// UI lets the user flip them individually if they want the memory /
// disk / context-window savings of the slimmer variant.

interface KiroSettingPayload { key?: string; value?: string }

// experimentalFlags is the single source of truth for which kiro-cli
// flags we expose in the UI. Adding a row here creates the toggle,
// its description, and the get/put wiring automatically.
const experimentalFlags: readonly {
  key: string;
  inputID: string;
}[] = [
  { key: "chat.enableCheckpoint", inputID: "flag-checkpoint" },
  { key: "chat.enableTodoList",   inputID: "flag-todolist" },
  { key: "chat.enableKnowledge",  inputID: "flag-knowledge" },
  { key: "hooks.showStatus",      inputID: "flag-hooks-status" },
  { key: "telemetry.enabled",     inputID: "flag-telemetry" },
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
      if (input === null) continue;
      const v = results[i]?.value ?? "";
      input.checked = v === "" || v === "true";
    }
  });
  for (let i = 0; i < experimentalFlags.length; i++) {
    const flag = experimentalFlags[i]!;
    const input = inputs[i] ?? null;
    if (input === null) continue;
    input.addEventListener("change", () => {
      showSaving();
      void setKiroSettingAction.dispatch({
        key: flag.key,
        value: input.checked ? "true" : "false",
        input,
      }, { silent: true })
        .then((r) => { if (r === null) showError(); else showSaved(); });
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
  { key: "compaction.excludeContextWindowPercent", inputID: "compact-context-buffer", isBool: false },
  // Chat retention (auto-archive lifetime). kiro-cli calls this
  // "cleanup.periodDays" and uses it both for its own session
  // cleanup and as the retention window for vibekit's archived
  // chats. 0 means "never clean up".
  { key: "cleanup.periodDays", inputID: "chat-retention-days", isBool: false },
];

function initCompactionSettings(): void {
  const inputs = compactionSettings.map(
    (s) => document.getElementById(s.inputID) as HTMLInputElement | null,
  );
  void Promise.all(
    compactionSettings.map((s) =>
      apiGet<KiroSettingPayload>(`/api/kiro-settings?key=${encodeURIComponent(s.key)}`),
    ),
  ).then((results) => {
    for (let i = 0; i < compactionSettings.length; i++) {
      const s = compactionSettings[i]!;
      const input = inputs[i] ?? null;
      if (input === null) continue;
      const v = results[i]?.value ?? "";
      if (s.isBool) {
        input.checked = v !== "true";
      } else {
        if (v !== "") input.value = v;
      }
    }
  });
  for (let i = 0; i < compactionSettings.length; i++) {
    const s = compactionSettings[i]!;
    const input = inputs[i] ?? null;
    if (input === null) continue;
    input.addEventListener("change", () => {
      let value: string;
      if (s.isBool) {
        value = input.checked ? "false" : "true";
      } else {
        value = input.value;
      }
      showSaving();
      void setKiroSettingAction.dispatch({
        key: s.key,
        value,
        input,
      }, { silent: true })
        .then((r) => { if (r === null) showError(); else showSaved(); });
    });
  }
}

// --- Default agent picker (Settings → Custom instructions) ---
//
// REMOVED: the picker exposed only two built-in agents (kiro_default,
// kiro_planner) that are already covered by the sidebar Build/Plan
// buttons. Subagents are ephemeral per-turn and don't belong here. If
// a custom agent is dropped into ~/.kiro/cli-agents/ inside the
// container, set it as default via `docker exec vibekit kiro-cli
// agent set-default <name>`.

// --- Debug logs toggle ---
//
// Separate from the kiro-cli experimental flags: this flips vibekit's
// own slog level via /api/settings rather than the kiro-cli settings
// endpoint. When on, server-side logs include slog.Debug entries;
// read them with `docker logs vibekit`.

function initDebugLogsToggle(initial: AppSettings): void {
  const input = document.getElementById("flag-debug-logs") as HTMLInputElement | null;
  if (input === null) return;
  input.checked = initial.debug_logs === true;
  input.addEventListener("change", () => {
    patchSettings({ debug_logs: input.checked }, input);
  });
}
