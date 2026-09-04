// ---------------------------------------------------------------------------
// Settings panel UI. Workspace preferences (last_model, notifications, theme,
// the file browser's path) live in server-side /api/settings; the three fields
// that are genuinely this SCREEN's live in device-view.ts.
// ---------------------------------------------------------------------------

import { initAllModals } from "./modals.js";
import { toggleSettingsView, toggleGitView } from "./tabs.js";
import { initGitPanel } from "./git.js";
import { getGitTab } from "./git-tabs.js";
import { restoreFileBrowser } from "./files.js";
import { restoreShell } from "./shell.js";
import { initTools, loadToolsList } from "./tools.js";
import { restoreNotifications } from "./notify.js";
import { loadSettings, patchSettings, initSettingsTracking } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { cacheTheme, cachedTheme, shellOpen } from "./device-view.js";
import type { ThemeChoice } from "./device-view.js";
import { applyThemeChoice, initThemeToggle } from "./theme.js";
import type { ThemeStorage } from "@cplieger/ui-primitives/theme";
import { initSettingsTabs } from "./settings-tabs.js";
import type { IdentityVerdict } from "./identity.js";
import { initPermissionsUI, initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { initMCP } from "./mcp-ui.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";
// (forge-auth.ts is imported by git-sources-tab.ts now; no settings-side
// import needed since the "Git & forges" Settings tab was retired.)
import { apiGet } from "./api-client.js";
import { loadVersions, getVersions } from "./versions.js";
import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import { initNotificationToggles } from "./settings-notifications.js";

import { showSaving, showSaved, showError, STEERING_SAVE_KEY } from "./save-indicator.js";
import { saveSteering, logout, setKiroSetting } from "./actions/settings.js";
import { runDiagnostics } from "./actions/tools.js";
import {
  bindLoadingState,
  registerCleanup,
  debouncedDispatch,
  subscribeByName,
} from "./actions/index.js";

// Per-key write generation for the kiro-cli settings endpoint; same rule as
// persist.ts's `keyGen`, which explains it. Separate because the key namespaces
// are (dotted kiro-cli keys against AppSettings keys).
let kiroSeq = 0;
const kiroGen = new Map<string, number>();

/**
 * Encapsulates the generation guard + showSaving/showSaved/showError lifecycle
 * for dispatching a kiro-cli setting change.
 *
 * Every remaining kiro-cli setting is a CHECKBOX, so there is no previous-value
 * parameter: the focus-time snapshot this used to carry existed for the two
 * compaction number fields, whose ACP counterparts turned out to have zero
 * readers upstream, and both fields are gone.
 */
function dispatchKiroSetting(key: string, value: string, input: HTMLInputElement): void {
  showSaving(key);
  const gen = ++kiroSeq;
  kiroGen.set(key, gen);
  void setKiroSetting.dispatch({ key, value, input }, { silent: true }).then((r) => {
    if (kiroGen.get(key) !== gen) {
      return;
    }
    if (r === null) {
      showError(key);
    } else {
      showSaved(key);
    }
  });
}

export type { EffectiveSettings } from "./persist.js";
export { loadSettings } from "./persist.js";

// --- The theme, and the paint cache that mirrors it ---
//
// The VALUE lives in config.json. The cache lives in this browser's
// localStorage, owned byte-wise by device-view.ts, and the POLICY is here —
// beside the value it mirrors, which is the only place both halves are visible
// at once.
//
// Three rules, and each one is why the cache is not a second source of truth:
//
//   1. Every write refreshes the cache in the same breath, so the NEXT load
//      paints the chosen theme before its fetch resolves rather than flashing
//      the old one.
//   2. Every settings load overwrites the in-memory choice from the server. The
//      server wins, always.
//   3. Before that load resolves, a read falls back to the cache. Something asks
//      for the theme that early on every load — the toggle wires itself during
//      chrome setup, well before checkAuthAndStart's fetch lands — and answering
//      "unset" would make the controller resolve the OS preference and then flip
//      when the real choice arrived.

/** The theme the server last reported, or the cache before it has. Module state
 *  rather than a read of EffectiveSettings, because the toggle is constructed before
 *  the settings fetch resolves (rule 3). */
let themeChoice: ThemeChoice | null = null;

/** Whether a settings payload has been folded in yet. Separates "the server says
 *  no theme is set" from "the server has not answered", which is the distinction
 *  the one-time adoption below turns on. */
let themeLoaded = false;

/** Set while a server value is being pushed into the live controller. The
 *  controller has one write verb and it means "the user chose this", so adopting
 *  a value the server just sent would go straight back out as a PATCH — and on
 *  the settings_updated path that PATCH re-broadcasts settings_updated. Same
 *  guard shape the old arrangement document used for the same reason: an echo
 *  that writes is a loop. */
let adoptingTheme = false;

function asThemeChoice(v: string | undefined): ThemeChoice | null {
  return v === "dark" || v === "light" || v === "system" ? v : null;
}

/** Push a choice into the live controller so the page repaints, without writing
 *  it back. */
function repaintTheme(choice: ThemeChoice): void {
  adoptingTheme = true;
  try {
    applyThemeChoice(choice);
  } finally {
    adoptingTheme = false;
  }
}

/** Fold a loaded settings payload's theme in, and carry the cache across ONCE
 *  when the server has none.
 *
 *  That adoption is the single value the deletion of the old whole-document
 *  arrangement carries over, and it is not a migration path: the document is
 *  gone and unread, while this cache is a live localStorage field the pre-paint
 *  snippet is still reading on every load. The theme is also the one loss a
 *  reader would SEE — on the very next load, as the wrong colour — so it is
 *  adopted rather than reset. Once, and only when nothing is set server-side, so
 *  a deliberate later change can never be overwritten by a stale cache.
 *
 *  Called on BOTH paths that learn a server theme: app.ts with the payload it
 *  already fetched at boot (a second GET to read one field would be a round trip
 *  for nothing), and the settings_updated handler, which is what makes a theme
 *  chosen on another device land here live — the behaviour the retired
 *  whole-document broadcast used to provide. */
export function adoptThemeFromSettings(s: EffectiveSettings): void {
  const fromServer = asThemeChoice(s.theme);
  const first = !themeLoaded;
  themeLoaded = true;
  if (fromServer !== null) {
    if (themeChoice !== fromServer) {
      themeChoice = fromServer;
      cacheTheme(fromServer);
      repaintTheme(fromServer);
    }
    return;
  }
  if (!first) {
    // The server has no theme and this is not the first answer, so there is
    // nothing to carry across and nothing new to learn. Adopting the cache again
    // would let a value the user has since cleared come back.
    return;
  }
  const carried = cachedTheme();
  themeChoice = carried;
  if (carried !== null) {
    // Write it through so the value stops being cache-only and starts
    // travelling to every other device, which is what it could not do before.
    void patchSettings({ theme: carried });
  }
}

/** The theme choice in force: the server's once it has answered, the paint cache
 *  until then. */
function currentTheme(): ThemeChoice | null {
  if (!themeLoaded && themeChoice === null) {
    return cachedTheme();
  }
  return themeChoice;
}

/** Record a chosen theme: server first (the authority), cache second (the paint
 *  hint), in-memory third so a read before the PATCH lands is still right. */
function setTheme(choice: ThemeChoice): void {
  themeChoice = choice;
  themeLoaded = true;
  cacheTheme(choice);
  if (adoptingTheme) {
    return;
  }
  void patchSettings({ theme: choice });
}

/** The adapter @cplieger/ui-primitives' createTheme persists through. Built here
 *  and INJECTED into initThemeToggle rather than reached from theme.ts, because
 *  this module already imports that one — a reverse import would be a cycle, and
 *  the direction is the reason this is a parameter.
 *
 *  Exported because it IS the boundary with the controller, and its `set` half is
 *  what the adopt guard above has to survive: a test that drives the adopt path
 *  without ever reaching this function proves nothing about the guard, which is
 *  how the guard first shipped un-covered. */
export const themeStorage: ThemeStorage = {
  get: () => currentTheme(),
  set: (value) => {
    setTheme(value === "light" || value === "system" ? value : "dark");
  },
};

/** @internal Test seam: forget the loaded theme so one case's choice is not the
 *  answer in the next. */
export function _resetThemeForTest(): void {
  themeChoice = null;
  themeLoaded = false;
  adoptingTheme = false;
}

/** Fetch settings from server and apply notification state only.
 *  Used for lightweight re-sync (e.g. after login) without touching
 *  per-device UI state. Compare with restoreAll() which also restores
 *  localStorage-based UI (shell, file browser, editor tabs). */
export async function syncSettings(): Promise<EffectiveSettings | null> {
  const s = await loadSettings();
  // Null means the fetch failed: seeding the dedup tracker from nothing would
  // clear it and re-arm the very write-back it exists to suppress, and applying
  // notification state from nothing would silence or unsilence push on a network
  // blip. Both callers (boot, and the settings_updated handler) tolerate a
  // no-op — the next frame or reload re-syncs.
  if (s === null) {
    return null;
  }
  // Seed the dedup tracker BEFORE any code path can fire patchSettings().
  // The bootstrap subscription fires (e.g. repo-picker.onSelectionChange)
  // would otherwise re-PATCH /api/settings with values it just loaded
  // from /api/settings, triggering the "Saving..." animation on every
  // page reload.
  initSettingsTracking(s);
  restoreNotifications(s);
  return s;
}

/** Restore all state: this device's own UI from device-view, workspace prefs
 *  from the loaded settings payload. Called once at startup. Unlike
 *  syncSettings(), this also restores shell, file browser, and editor tabs.
 *  (Theme is applied separately by initThemeToggle() during initUI.) */
export function restoreAll(s: EffectiveSettings): void {
  if (shellOpen()) {
    restoreShell();
  }
  if (s.fb_path !== "") {
    restoreFileBrowser(s.fb_path);
  }
  // Editor tabs are NOT restored from here any more, and there is no second list
  // of open paths to restore them from: an editor tab's path IS its subject's
  // `ref`, so the tab set that `listTabs` adopts at boot already names every open
  // file. `ui-state.editor_files` existed only to recover a path from a synthetic
  // `editor:<path>` id.

  restoreNotifications(s);
  // Theme is applied by initThemeToggle() (initUI), which constructs the
  // createTheme controller — it reads through the storage adapter above and
  // applies the resolved theme on construction. No separate apply is needed
  // here; what IS needed is adoptThemeFromSettings, called by app.ts the moment
  // the payload lands so the server's choice replaces the paint cache.
  initPermissionsUI(s);
  initNativePolicyUI();
  initDebugLogsToggle(s);
  initAgentCapabilities(s);
  initChatRetention(s);
}

// --- Chat retention (vibekit-owned; /api/settings chat_retention_days) ---
//
// kiro-cli's cleanup.periodDays is pinned to 0/never — vibekit owns retention
// end to end. The Days-kept number field carries 0 (off) .. N (keep N days);
// the Keep-forever checkbox overrides it to -1 (kept, never purged) and HIDES
// the Days-kept row. Hiding rather than disabling: -1 has no day count, so a
// greyed-out field still showing the last number reads as the value in force.
// The input keeps that number in the DOM, so unchecking restores it.
// Writes go to /api/settings; the settings_updated SSE refreshes retention.ts
// (keep-vs-delete-on-close + History visibility).
//
// The row carries the whole field (label + input), and it sits BELOW the
// checkbox so revealing it moves nothing the reader is pointing at. Hiding
// goes through the `.hidden` utility rather than the `hidden` attribute:
// `.section-option` declares `display: flex`, which beats the UA
// `[hidden] { display: none }` rule.
export function initChatRetention(s: EffectiveSettings): void {
  const daysInput = document.getElementById("chat-retention-days") as HTMLInputElement | null;
  const daysRow = document.getElementById("chat-retention-days-row");
  const foreverInput = document.getElementById("chat-retention-forever") as HTMLInputElement | null;
  if (daysInput === null || foreverInput === null) {
    return;
  }
  const showDaysRow = (forever: boolean): void => {
    daysRow?.classList.toggle("hidden", forever);
  };
  // No coalesce: the field is required on the payload and the server resolved
  // its default. The mirror that used to sit here is why this change exists.
  const current = s.chat_retention_days;
  foreverInput.checked = current === -1;
  showDaysRow(current === -1);
  if (current >= 0) {
    daysInput.value = String(current);
  }

  const persist = (value: number, input: HTMLInputElement): void => {
    void patchSettings({ chat_retention_days: value }, input);
  };

  daysInput.addEventListener("change", () => {
    if (foreverInput.checked) {
      return; // forever wins; the Days-kept row is hidden
    }
    const n = parseInt(daysInput.value, 10);
    persist(!isNaN(n) && n >= 0 ? n : 0, daysInput);
  });
  foreverInput.addEventListener("change", () => {
    showDaysRow(foreverInput.checked);
    if (foreverInput.checked) {
      persist(-1, foreverInput);
      return;
    }
    const n = parseInt(daysInput.value, 10);
    // Input validation rather than an absent-key fallback: the box can hold
    // empty or non-numeric text when Keep-forever is unchecked. The value it
    // falls back to is the one the SERVER sent for this key, not a constant
    // restated here.
    persist(!isNaN(n) && n >= 0 ? n : s.chat_retention_days, foreverInput);
  });
}

// --- UI init ---

/** Load the one list the Instructions tab still shows: the workspace knowledge
 *  bases. Fired once on the tab's first activation via the settings-tabs loader
 *  map.
 *
 *  TWO lists left this panel, and for the same reason both times: a `.kiro`
 *  inventory belongs on the page that shows `.kiro` inventories. The steering /
 *  skills / agents list went to the configuration browser first, and the HOOKS
 *  dashboard followed it — hooks are `.kiro` files with a trigger, and keeping
 *  them here meant one file family had two homes with different affordances in
 *  each. The /api/workspace/kiro-config ENDPOINT stays: role-picker.ts fetches it
 *  to seed the mode picker with workspace agents before a session exists. */
function loadInstructionsPanel(): void {
  loadKnowledge();
}

/** Read what the General panel's controls display. Fired once, on the panel's
 *  first activation, via the settings-tabs loader map.
 *
 *  The experimental toggles are the reason there is a loader here at all: each
 *  one is a `GET /api/kiro-settings`, and each of those is a `kiro-cli settings`
 *  SPAWN with its own 3 s budget on the server. Three of them fired from
 *  `initUI()` at boot, concurrent with the boot's own reads, to fill checkboxes
 *  in a panel nobody had opened. */
function loadGeneralPanel(): void {
  initExperimentalToggles();
}

export function initUI(): void {
  initThemeToggle(themeStorage);

  // Settings gear opens the tabbed Settings panel. Default tab is General;
  // deep-link URLs (e.g. /settings/tools) override this via applyRoute.
  // Panel data loads lazily on each tab's first activation (see the loader
  // map below) — the gear no longer preloads the Tools list while opening
  // the General panel (B9).
  $.settingsBtn.addEventListener("click", () => {
    void toggleSettingsView("general");
  });

  // Per-tab lazy data loaders: fired by settings-tabs on the first ACTIVATION of
  // each tab, never on the subscribe-time paint that shows the default panel.
  // Every tab has one now — General's is what took its three kiro-cli spawns off
  // the boot path.
  initSettingsTabs({
    general: loadGeneralPanel,
    tools: loadToolsList,
    permissions: loadNativePolicy,
    instructions: loadInstructionsPanel,
  });
  initSteeringEditor();
  initLogoutButton();
  initNotificationToggles();
  initDiagnostics();

  initTools();
  initMCP();
  initKnowledge();
  initAllModals();

  // The "Git & forges" tab in Settings was retired with the multi-repo
  // git-page rewrite — forge accounts now live on the Sources tab of
  // the git view. So there's no longer a tab-change → load mapping
  // here. (forge-auth.ts is still imported because it powers the
  // accounts UI inside that Sources tab.)

  $.gitBtn.addEventListener("click", () => {
    // Open to whichever sub-tab is currently active (defaults to "changes" on
    // first open) so the URL the tab pushes matches the visible panel. No loader
    // callback: the tab factory reaches `loadGitRepos` through a lazy import, so
    // /git opened from a path link refreshes its repos exactly as the sidebar's
    // door does — a divergence that was real before the factory existed.
    void toggleGitView(getGitTab());
  });
}

/** Post-auth UI init: the reads that must not fire on a login screen.
 *
 *  `loadAbout` reads the shared version pair (versions.ts owns GET /api/version).
 *  `initGitPanel` wires the git view and its sidebar badge, whose subscription starts
 *  git-status-store.ts's ONE status-all scan and forge-store.ts's 15s forges poll.
 *
 *  Called once, through `boot.ts`'s `initPostAuth` — on the `signed_in` AND
 *  `unavailable` verdicts at boot, and from `onLoginSuccess` after a first login. */
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
      showSaved(STEERING_SAVE_KEY);
    } else if (inst.status === "error") {
      showError(STEERING_SAVE_KEY);
    }
  });

  textarea.addEventListener("input", () => {
    showSaving(STEERING_SAVE_KEY);
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

/** Render the About grid from the shared version pair.
 *
 *  It no longer fetches: `versions.ts` owns GET /api/version, because the sidebar
 *  status card names both values too and a second request for the same two
 *  strings is a second thing that can disagree. Both are passive until the
 *  container restarts, so one read per page load serves every reader. */
async function loadAbout(): Promise<void> {
  await loadVersions();
  const v = getVersions();
  const vibekitEl = document.getElementById("about-vibekit");
  const kiroEl = document.getElementById("about-kirocli");
  if (vibekitEl !== null) {
    vibekitEl.textContent = v.vibekit === "" ? "—" : v.vibekit;
  }
  if (kiroEl !== null) {
    kiroEl.textContent = v.kiroCli === "" ? "—" : v.kiroCli;
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

// --- User display ---

/** The status card's auth line, one phrase per arm. `unavailable` gets its own:
 *  reading "not signed in" for it is the mistake the third arm exists to remove. */
const AUTH_LINE: Readonly<Record<IdentityVerdict["state"], string>> = {
  signed_in: "signed in",
  signed_out: "not signed in",
  unavailable: "unknown",
};

/** Paint the sidebar identity row and the status card's auth line.
 *
 *  Takes the VERDICT rather than an email because three answers reach it and only
 *  one carries an address. Writing `textContent` also drops the authored pending
 *  shimmer (index.html #user-email), so every arm resolves the region. */
export function renderIdentity(v: IdentityVerdict): void {
  $.userEmail.textContent = v.state === "signed_in" ? v.email : "";
  $.stAuth.textContent = AUTH_LINE[v.state];
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
//
// A key belongs here only if it has a kiro-cli-SIDE role, because KAS's ACP path
// reads no kiro-cli setting at all: measured on 2.19.2, the bundle contains zero
// occurrences of `cli.json`, `kiro-cli/settings`, `readSettingsFile` or
// `loadCliSettings`, and each `chat.*` literal appears exactly once, as a
// `@see kiro-cli:` cross-reference inside the settings schema. So a write here
// reaches the TUI and the index builder, never a vibekit chat. Anything that has
// to change a vibekit chat goes through `_meta.kiro.settings` instead — the
// kascap table's door — which is where tool search and knowledge now send.
//
// `chat.enableCheckpoint` and `chat.enableTodoList` were REMOVED from this list
// for the same reason and one more: their ACP counterparts (`checkpoint`,
// `todoList`) are declared in KAS's own settings schema with ZERO readers in any
// of its three reader shapes, so neither key could change a chat through either
// door. Five siblings share that property (`thinking`, `tangentMode`,
// `_subagent`, `_delegate`, `compaction`); `internal/kascap/table.go` carries a
// withholding row for each.
//
// `chat.enableKnowledge` and `toolSearch.enabled` left for the OPPOSITE reason:
// their ACP counterparts ARE read, so the controls were pointed at the wrong door
// rather than being inert. Both moved to initAgentCapabilities below, which writes
// vibekit's own settings and reaches the agent through kascap's gates.
const experimentalFlags: readonly {
  key: string;
  inputID: string;
  inverted?: boolean;
}[] = [
  { key: "hooks.showStatus", inputID: "flag-hooks-status" },
  { key: "telemetry.enabled", inputID: "flag-telemetry" },
  // Checked = disable inheritance of default steering/skills/AGENTS.md by
  // custom agents (kiro-cli 2.10+). Not inverted: on = true = disabled.
  // Seeded false in entrypoint so the unset->on fallback doesn't mis-render.
  { key: "chat.disableInheritingDefaultResources", inputID: "flag-disable-inherit-resources" },
];

export function initExperimentalToggles(): void {
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
}

// --- Agent capabilities (Settings → General) ---
//
// Two toggles that look like the kiro-cli flags above and are a different
// mechanism. They write VIBEKIT settings through /api/settings, and
// internal/agent resolves each at spawn time into the `_meta.kiro.settings`
// handshake keys KAS actually reads (`knowledge` plus its capability twin, and
// `toolSearch`).
//
// They used to be `chat.enableKnowledge` and `toolSearch.enabled` in the list
// above, which measured as unable to reach a running chat: KAS's ACP path reads no
// kiro-cli setting anywhere, so the knowledge switch appeared to turn knowledge
// off and did nothing at all. Each control kept its meaning and changed door.
//
// Neither is live. KAS resolves both when a session is created and freezes the
// answer for that session's life, which is why the section hint says new
// conversations rather than leaving a user to discover it.
// There is no `fallback` column any more. knowledge_enabled's was `true`,
// mirroring the server's default because an absent key read as the zero value
// would take the knowledge tool away from every existing install — and the case
// it covered was "a settings payload written before the key existed", which the
// resolved GET no longer produces. The server states every one of these.
const agentCapabilities: readonly {
  key: "knowledge_enabled" | "tool_search_enabled" | "memory_enabled";
  inputID: string;
}[] = [
  { key: "knowledge_enabled", inputID: "flag-knowledge" },
  { key: "tool_search_enabled", inputID: "flag-tool-search" },
  // memory_enabled is off by standing veto, and off is not a quiet state on the
  // wire: the server still SENDS the veto, because an absent key reads to
  // kiro-cli as "let the experiment decide".
  { key: "memory_enabled", inputID: "flag-memory" },
];

function initAgentCapabilities(initial: EffectiveSettings): void {
  for (const cap of agentCapabilities) {
    const input = document.getElementById(cap.inputID) as HTMLInputElement | null;
    if (input === null) {
      continue;
    }
    input.checked = initial[cap.key];
    input.addEventListener("change", () => {
      void patchSettings({ [cap.key]: input.checked }, input);
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

function initDebugLogsToggle(initial: EffectiveSettings): void {
  const input = document.getElementById("flag-debug-logs") as HTMLInputElement | null;
  if (input === null) {
    return;
  }
  input.checked = initial.debug_logs;
  input.addEventListener("change", () => {
    void patchSettings({ debug_logs: input.checked }, input);
  });
}
