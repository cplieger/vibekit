// ---------------------------------------------------------------------------
// The UI arrangement: which tabs are open, in what order, which are pinned,
// which files the editor holds, the file browser's path, the theme, the two drag
// heights, dismissed banners and per-turn fold overrides.
//
// SERVER-OWNED, since 2026-08. It used to live entirely in localStorage on the
// reasoning that an arrangement is a per-viewer preference, and that reasoning
// was wrong in the way the sibling terminal app already learned: web-terminal-ui
// shipped a `wt-tab-order` localStorage key on exactly that argument, the user
// reported the arrangement not travelling between devices, and the fix was to
// DELETE the local copy rather than sync it both ways. Its steering doc carries
// the standing rule this file now obeys — do not reintroduce a local arrangement
// as an offline fallback, because two sources of truth for one ordering is how
// the original bug got its per-load reshuffle.
//
// TWO FIELDS STAY LOCAL, and they are the exceptions the user named:
//
//   - `active_view` — which tab THIS screen is looking at. A phone must not move
//     the desktop's active tab.
//   - `shell_open` — whether the terminal panel is showing. The shell's CONTENT
//     already travels without help: it is one global PTY on the server that
//     replays its screen and scrollback on connect, so a second device sees the
//     same terminal whether or not the panel was open when it arrived.
//   - `shell_h` — how tall that panel is dragged. A length is the one thing here
//     whose right value genuinely depends on the screen in front of you: 700px
//     is two thirds of a laptop and the whole of a phone. Per-device, and losing
//     it is not a loss worth engineering against.
//
// `theme` is server-owned but ALSO cached locally, and that is not a second
// source of truth: the inline pre-paint snippet has to pick a theme before any
// fetch can resolve, so the cache is a paint-time hint refreshed from the server
// on every load. The server always wins.
//
// SHAPE OF THE API. `load()` stays synchronous and `save()` stays fire-and-
// forget, because ~40 call sites read and write this state during layout and a
// promise at each would be a rewrite of all of them. What changed underneath is
// where the bytes live: an in-memory document hydrated once at boot, published
// back with a debounced PUT that carries the revision it was based on.
// ---------------------------------------------------------------------------

import { LS_UI_STATE_KEY } from "./ls-keys.js";

const LS_KEY = LS_UI_STATE_KEY;

/** How long a burst of local mutations is allowed to coalesce before it is
 *  published. Long enough that a tab drag, which rewrites the order on every
 *  commit, costs one request; short enough that a device switched to seconds
 *  later sees the change. */
const PUBLISH_DEBOUNCE_MS = 400;

export interface UIState {
  tab_order: string[];
  /** Top-level tabs the user pinned, so a long-running conversation stays
   *  reachable when fifteen tabs are open. */
  pinned_tabs: string[];
  /** LOCAL. Which tab this screen is looking at; see the header. */
  active_view: string;
  /** LOCAL. Whether the terminal panel is showing; see the header. */
  shell_open: boolean;
  /** LOCAL. User-dragged shell panel height in px; 0 = the CSS default (16rem).
   *  Per-device: see the header. */
  shell_h: number;
  editor_files: string[];
  fb_path: string;
  theme: "dark" | "light" | "system" | null;
  dismissed_banners: string[];
  /** Per-chat, per-turn fold overrides: chat id → turn's opening message id →
   *  open. Absent means "follow the automatic rule", which is why the value is a
   *  boolean rather than a set of open ids — a turn the reader deliberately
   *  FOLDED has to outrank the two-newest rule too. */
  turn_folds: Record<string, Record<string, boolean>>;
}

/** The fields this device keeps to itself. Named once, so `save` and `hydrate`
 *  cannot disagree about where a field lives. */
const LOCAL_FIELDS = ["active_view", "shell_open", "shell_h"] as const;

function empty(): UIState {
  return {
    tab_order: [],
    pinned_tabs: [],
    active_view: "",
    shell_open: false,
    shell_h: 0,
    editor_files: [],
    fb_path: "",
    theme: null,
    dismissed_banners: [],
    turn_folds: {},
  };
}

/** The live document. Authoritative for the synced fields between a hydrate and
 *  the next remote change; the local fields are read through from localStorage
 *  so a second tab in the same browser sees them. */
let doc: UIState = empty();
/** The revision the synced half of `doc` is based on. */
let revision = 0;
let hydrated = false;
let publishTimer: ReturnType<typeof setTimeout> | null = null;
/** Set while applying a remote document, so the resulting save does not bounce
 *  straight back to the server as a write. */
let applying = false;

type Listener = (s: UIState) => void;
const listeners = new Set<Listener>();

/** Subscribe to arrangement changes that did NOT come from this device. The tab
 *  strip uses it to converge on a remote reorder. */
export function onRemoteChange(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function load(): UIState {
  const s: UIState = { ...doc, ...readLocal() };
  if (s.theme === null && !hydrated) {
    // Before the first GET resolves, the paint cache is the only answer there
    // is — and something asks for the theme that early on every load (the theme
    // toggle wires itself during chrome setup). Returning null would make the
    // controller resolve the OS preference and then flip when the server's real
    // choice landed. Reading the cache is not a second source of truth: it is
    // written from the server's value and the server always wins after hydrate.
    s.theme = cachedTheme();
  }
  return s;
}

/** The current revision, for a caller that needs to publish explicitly. */
export function currentRevision(): number {
  return revision;
}

export function save(patch: Partial<UIState>): void {
  doc = { ...doc, ...patch };
  writeLocal(patch);
  if ("theme" in patch) {
    // Refresh the paint cache in the same breath, so the NEXT load picks the
    // chosen theme before its fetch resolves rather than flashing the old one.
    cacheTheme(doc.theme);
  }
  if (applying) {
    // This save is the tail of applyRemote: the values came FROM the server, so
    // publishing them would be an echo, and an echo that bumps the revision
    // makes every other device's next write stale for a change nobody made.
    return;
  }
  schedulePublish();
}

/** Read the arrangement from the server. Called once at boot, before tabs are
 *  restored. Never throws: an unreachable server leaves the in-memory document
 *  empty, which opens no tabs rather than opening the wrong ones. */
export async function hydrate(): Promise<void> {
  try {
    const r = await fetch("/api/ui-state", { headers: { Accept: "application/json" } });
    if (!r.ok) {
      hydrated = true;
      return;
    }
    const raw: unknown = await r.json();
    adopt(raw);
  } catch {
    // Offline or a dead endpoint. Deliberately no localStorage fallback for the
    // synced fields: see the header.
  }
  hydrated = true;
}

/** Apply a document the SERVER sent (the boot GET, a 409 body, or the
 *  ui_state_changed broadcast). Returns true when anything changed. */
export function applyRemote(raw: unknown): boolean {
  const before = JSON.stringify(syncedOnly(doc));
  if (!adopt(raw)) {
    return false;
  }
  if (JSON.stringify(syncedOnly(doc)) === before) {
    // The writer's own echo. Nothing to repaint, and repainting anyway is how a
    // tab that was just dragged snaps back to where it started.
    return false;
  }
  applying = true;
  try {
    for (const fn of listeners) {
      fn(load());
    }
  } finally {
    applying = false;
  }
  return true;
}

/** Fold a server document into the in-memory one, honouring the revision so a
 *  late-arriving older document cannot overwrite a newer one. */
function adopt(raw: unknown): boolean {
  if (typeof raw !== "object" || raw === null) {
    return false;
  }
  const o = raw as Record<string, unknown>;
  const rev =
    typeof o["revision"] === "number" && Number.isFinite(o["revision"]) ? o["revision"] : 0;
  if (hydrated && rev < revision) {
    return false;
  }
  revision = rev;
  const s = sanitize(o);
  // The local fields never come from the server, so they are not overwritten
  // even if a stray document carries them.
  doc = { ...s, ...readLocal() };
  cacheTheme(doc.theme);
  return true;
}

function schedulePublish(): void {
  if (publishTimer !== null) {
    clearTimeout(publishTimer);
  }
  publishTimer = setTimeout(() => {
    publishTimer = null;
    void publish();
  }, PUBLISH_DEBOUNCE_MS);
}

/** Send the synced half, then adopt whatever the server answers.
 *
 *  A 409 is not retried by re-sending: this device's document is the stale one,
 *  so it adopts the server's and lets the next real mutation publish. That is the
 *  same discipline the terminal engine's order endpoint uses, and for the same
 *  reason — re-sending is a fight the stale writer cannot win. */
async function publish(): Promise<void> {
  if (!hydrated) {
    // Publishing before the first GET would write an empty arrangement over a
    // real one. Try again once hydration lands.
    schedulePublish();
    return;
  }
  const body = JSON.stringify({ ...syncedOnly(doc), revision });
  try {
    const r = await fetch("/api/ui-state", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body,
    });
    if (r.status === 409 || r.ok) {
      applyRemote(await r.json());
    }
  } catch {
    // The arrangement is not lost data and a toast for it would be noise. The
    // next mutation republishes.
  }
}

/** Publish immediately, for a page that is going away. */
export function flush(): void {
  if (publishTimer === null) {
    return;
  }
  clearTimeout(publishTimer);
  publishTimer = null;
  void publish();
}

function syncedOnly(s: UIState): Omit<UIState, (typeof LOCAL_FIELDS)[number]> {
  const { active_view: _a, shell_open: _s, shell_h: _h, ...rest } = s;
  return rest;
}

// --- The two local fields, plus the theme paint cache ---

interface LocalState {
  active_view: string;
  shell_open: boolean;
  shell_h: number;
}

function emptyLocal(): LocalState {
  return { active_view: "", shell_open: false, shell_h: 0 };
}

function readLocal(): LocalState {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw === null) {
      return emptyLocal();
    }
    const o = JSON.parse(raw) as Record<string, unknown>;
    return {
      active_view: typeof o["active_view"] === "string" ? o["active_view"] : "",
      shell_open: typeof o["shell_open"] === "boolean" ? o["shell_open"] : false,
      shell_h: nonNegative(o["shell_h"]) ?? 0,
    };
  } catch {
    return emptyLocal();
  }
}

function writeLocal(patch: Partial<UIState>): void {
  const touched = LOCAL_FIELDS.some((k) => k in patch);
  if (!touched) {
    return;
  }
  try {
    const cur = readLocal();
    const next: LocalState & { theme?: string } = {
      active_view: patch.active_view ?? cur.active_view,
      shell_open: patch.shell_open ?? cur.shell_open,
      shell_h: patch.shell_h ?? cur.shell_h,
    };
    const theme = cachedTheme();
    if (theme !== null) {
      next.theme = theme;
    }
    localStorage.setItem(LS_KEY, JSON.stringify(next));
  } catch {
    // ignore quota / disabled storage
  }
}

/** Keep the theme in localStorage too, so the inline pre-paint snippet has an
 *  answer before any fetch resolves. A CACHE, never a source of truth. */
function cacheTheme(theme: UIState["theme"]): void {
  try {
    const raw = localStorage.getItem(LS_KEY);
    const o = raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
    if (theme === null) {
      delete o["theme"];
    } else {
      o["theme"] = theme;
    }
    localStorage.setItem(LS_KEY, JSON.stringify(o));
  } catch {
    // ignore
  }
}

function cachedTheme(): UIState["theme"] {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw === null) {
      return null;
    }
    const t = (JSON.parse(raw) as Record<string, unknown>)["theme"];
    return t === "dark" || t === "light" || t === "system" ? t : null;
  } catch {
    return null;
  }
}

/** Validate a server document field by field instead of shallow-spreading it:
 *  stale or hand-edited data (a string where an array is expected, a non-finite
 *  height) previously flowed straight into tab restore and the shell sizing
 *  math. Any invalid field falls back to its default; valid siblings are kept. */
function sanitize(o: Record<string, unknown>): UIState {
  const e = empty();
  return {
    tab_order: strArray(o["tab_order"]) ?? e.tab_order,
    pinned_tabs: strArray(o["pinned_tabs"]) ?? e.pinned_tabs,
    active_view: e.active_view,
    shell_open: e.shell_open,
    shell_h: e.shell_h,
    editor_files: strArray(o["editor_files"]) ?? e.editor_files,
    fb_path: typeof o["fb_path"] === "string" ? o["fb_path"] : e.fb_path,
    // "system" is a real stored CHOICE, not the absence of one: it means the
    // user asked to follow the OS. Dropping it here is what made Auto
    // unreachable after one toggle click, since the value round-tripped to null
    // and the toggle only ever wrote the two concrete themes.
    theme:
      o["theme"] === "dark" || o["theme"] === "light" || o["theme"] === "system"
        ? o["theme"]
        : null,
    dismissed_banners: strArray(o["dismissed_banners"]) ?? e.dismissed_banners,
    turn_folds: foldMap(o["turn_folds"]) ?? e.turn_folds,
  };
}

function nonNegative(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined;
}

/** Validate the nested fold map. Hand-edited or stale data must not reach the
 *  renderer's open/closed decision, so anything not string→string→boolean is
 *  dropped at its own level rather than failing the whole state. */
function foldMap(v: unknown): Record<string, Record<string, boolean>> | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    return undefined;
  }
  const out: Record<string, Record<string, boolean>> = {};
  for (const [chatID, byTurn] of Object.entries(v as Record<string, unknown>)) {
    if (typeof byTurn !== "object" || byTurn === null || Array.isArray(byTurn)) {
      continue;
    }
    const inner: Record<string, boolean> = {};
    for (const [turnID, open] of Object.entries(byTurn as Record<string, unknown>)) {
      if (typeof open === "boolean") {
        inner[turnID] = open;
      }
    }
    if (Object.keys(inner).length > 0) {
      out[chatID] = inner;
    }
  }
  return out;
}

function strArray(v: unknown): string[] | undefined {
  return Array.isArray(v) && v.every((x) => typeof x === "string") ? v : undefined;
}

/** @internal Test seam: reset the module to a fresh, unhydrated state. */
export function _resetForTest(): void {
  doc = empty();
  revision = 0;
  hydrated = false;
  applying = false;
  if (publishTimer !== null) {
    clearTimeout(publishTimer);
    publishTimer = null;
  }
  listeners.clear();
}
