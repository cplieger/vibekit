// The per-device localStorage fields. ONE OWNER of the `vibekit.ui-state` key,
// and no second writer may be added: every write is a read-modify-write of one
// JSON blob, so a second module doing its own drops whatever landed between its
// read and its write. The key name cannot change either — nothing is migrated.
//
// `static/index.html`'s inline pre-paint snippet reads this blob's `theme`
// before any module loads; `theme-init-snippet.test.ts` pins both names.

import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** The recorded theme CHOICE. "system" is a real choice — the user asked to
 *  follow the OS — which is why it is a value here and not the absence of one. */
export type ThemeChoice = "dark" | "light" | "system";

/** Which pointer this screen is driven by. "coarse" is a finger or a pen,
 *  "fine" a mouse, a trackpad or a stylus with a cursor. */
export type PointerTier = "fine" | "coarse";

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

/** The cached theme choice, or null when none was ever written. A CACHE of
 *  `config.json`'s value: it answers which theme to paint before the settings
 *  response arrives, and never outranks it. */
export function cachedTheme(): ThemeChoice | null {
  const t = readBlob()["theme"];
  return t === "dark" || t === "light" || t === "system" ? t : null;
}

/** The last pointer tier OBSERVED on this screen, or null when none has been.
 *  The middle rung of `pointer-tier.ts`'s resolution: worth more than the
 *  capability guess below it, less than a choice the user stated. */
export function cachedPointerTier(): PointerTier | null {
  const t = readBlob()["pointer"];
  return t === "fine" || t === "coarse" ? t : null;
}

export function cachePointerTier(tier: PointerTier): void {
  writeBlob({ pointer: tier });
}

/** The tier the user CHOSE with the toggle, or null when they never have. The
 *  top rung, and a separate field from `pointer` so no input event can overturn
 *  it: a detector writing the same field would erase the choice on the first
 *  mouse move. */
export function pointerModeChoice(): PointerTier | null {
  const t = readBlob()["pointer_mode"];
  return t === "fine" || t === "coarse" ? t : null;
}

export function setPointerModeChoice(tier: PointerTier): void {
  writeBlob({ pointer_mode: tier });
}

/** Whether a coarse pointer has EVER driven this screen. Sticky and never
 *  cleared, because it is what reveals the touch/mouse toggle. A non-boolean
 *  value reads false, so a hand-edited blob cannot reveal the control. */
export function coarseEverSeen(): boolean {
  return readBlob()["pointer_coarse_seen"] === true;
}

export function markCoarseSeen(): void {
  writeBlob({ pointer_coarse_seen: true });
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
