// ---------------------------------------------------------------------------
// The three fields that are THIS SCREEN's, plus the theme's paint cache.
//
// Everything about which tabs are open and how they are arranged is server-owned
// (`internal/tabs`, projected by `tabs.ts`). These are the members that were
// never about the workspace at all, and each is here for its own stated reason:
//
//   - `active_view` — which tab this screen is looking at. A phone must not move
//     the desktop's active tab.
//   - `shell_open` — whether the terminal panel is showing. The shell's CONTENT
//     already travels without help: it is one global PTY on the server that
//     replays screen and scrollback on connect, so a second device sees the same
//     terminal whether or not the panel was open when it arrived. The panel can
//     start closed.
//   - `shell_h` — how tall that panel is dragged. A length is the one value here
//     whose right answer genuinely depends on the screen in front of you: 700px
//     is two thirds of a laptop and the whole of a phone. Losing it is not a
//     loss worth engineering against.
//
// # Why this module exists rather than the fields living where they used to
//
// They were three fields inside `ui-state.ts`, a module whose other job was
// mirroring a whole-document server arrangement — and that document is gone, so
// they needed a home that is only about them. Having one is also what makes the
// ownership rule below expressible.
//
// # ONE owner of the localStorage key, and that is the rule to keep
//
// The `vibekit.ui-state` key holds four fields and this module is the only writer
// of any of them. It has to be: every write is a read-modify-write of one JSON
// blob, so a second module doing its own read-modify-write drops whatever landed
// between its read and its write. `ui-state.ts` had exactly that shape — a
// `writeLocal` for the device fields beside a `cacheTheme` for the theme — and it
// only worked because `writeLocal` remembered to re-read and re-attach the theme
// by hand. The key name is unchanged deliberately: nothing is migrated, and a
// rename would silently reset every reader's shell height and theme.
//
// # The theme field is a CACHE, and it is the one field with a second reader
//
// The inline pre-paint snippet in `static/index.html` reads this blob's `theme`
// field before any module loads and before any fetch can resolve, which is what
// stops a wrong-theme flash on every load. The AUTHORITY for the theme is
// `config.json` (`settings.ts` owns the policy: refresh the cache on every
// change, and adopt it once when the server has none). This module owns only the
// bytes. `theme-init-snippet.test.ts` pins the snippet against the key and the
// field name, so both must stay as they are.
// ---------------------------------------------------------------------------

import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** The recorded theme CHOICE. "system" is a real choice — the user asked to
 *  follow the OS — which is why it is a value here and not the absence of one. */
export type ThemeChoice = "dark" | "light" | "system";

/** The three fields, as one record. Read together because they are stored
 *  together; written one at a time because that is how they change. */
interface DeviceView {
  active_view: string;
  shell_open: boolean;
  shell_h: number;
}

function empty(): DeviceView {
  return { active_view: "", shell_open: false, shell_h: 0 };
}

/** The whole blob, or an empty object. Never throws: storage can be disabled
 *  outright (a private window, a locked-down profile), and a device preference is
 *  not worth a boot failure. */
function readBlob(): Record<string, unknown> {
  try {
    const raw = localStorage.getItem(LS_UI_STATE_KEY);
    if (raw === null) {
      return {};
    }
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

/** Merge `patch` into the blob and write it back. The read-modify-write is here,
 *  once, for the reason in the header. */
function writeBlob(patch: Record<string, unknown>): void {
  try {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ ...readBlob(), ...patch }));
  } catch {
    // Quota, or storage disabled. Nothing here is recoverable state.
  }
}

/** Every field validated rather than spread: a hand-edited blob or one written
 *  by an older build can carry a string where a number belongs, and `shell_h`
 *  feeds the panel's sizing arithmetic directly. An invalid field falls back to
 *  its default while its valid siblings are kept. */
export function loadDeviceView(): DeviceView {
  const o = readBlob();
  const e = empty();
  const h = o["shell_h"];
  return {
    active_view: typeof o["active_view"] === "string" ? o["active_view"] : e.active_view,
    shell_open: typeof o["shell_open"] === "boolean" ? o["shell_open"] : e.shell_open,
    shell_h: typeof h === "number" && Number.isFinite(h) && h >= 0 ? h : e.shell_h,
  };
}

/** Which tab this screen was last looking at. "" when nothing is recorded. */
export function activeView(): string {
  return loadDeviceView().active_view;
}

export function setActiveView(id: string): void {
  writeBlob({ active_view: id });
}

/** Whether the terminal panel was showing. */
export function shellOpen(): boolean {
  return loadDeviceView().shell_open;
}

export function setShellOpen(open: boolean): void {
  writeBlob({ shell_open: open });
}

/** The dragged panel height in px; 0 means "the CSS default" (16rem). */
export function shellHeight(): number {
  return loadDeviceView().shell_h;
}

export function setShellHeight(px: number): void {
  writeBlob({ shell_h: px });
}

/** The cached theme choice, or null when none was ever written.
 *
 *  A CACHE of `config.json`'s value, never a source of truth — see the header.
 *  It answers the one question a fetch cannot: which theme to paint before the
 *  first byte of the settings response arrives. */
export function cachedTheme(): ThemeChoice | null {
  const t = readBlob()["theme"];
  return t === "dark" || t === "light" || t === "system" ? t : null;
}

/** Refresh the cache so the NEXT load paints the right theme before its fetch
 *  resolves. `null` clears the field, which makes the snippet fall back to the OS
 *  preference — the same answer an absent server value gets. */
export function cacheTheme(theme: ThemeChoice | null): void {
  if (theme === null) {
    const o = readBlob();
    delete o["theme"];
    try {
      localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify(o));
    } catch {
      // See writeBlob.
    }
    return;
  }
  writeBlob({ theme });
}
