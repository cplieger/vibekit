// ---------------------------------------------------------------------------
// The build versions: vibekit's own, and the kiro-cli it is running.
//
// ONE owner of GET /api/version, because three surfaces name these now: the
// sidebar status card's connection line and its agent-runtime line, and Settings
// → About. `settings.ts` used to fetch it privately, so the status card had no
// way to reach the pair without a second request for the same two strings.
//
// Fetched ONCE per page load. Both values are properties of the running
// container — vibekit's is baked at build (`internal/version.Build`) and
// kiro-cli's comes from a `--version` subprocess the server spawns per request —
// so neither can change without a restart, and a restart is a fresh page.
//
// Signal-backed rather than a plain getter because the readers paint before the
// fetch resolves: the status card is built during boot and the transport reports
// `connected` within milliseconds, long before a subprocess has answered. An
// effect over this is what lets those lines pick the version up when it lands
// instead of staying version-less until the next status change.
// ---------------------------------------------------------------------------

import { signal, type Signal } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";

/** The wire shape of GET /api/version (internal/server/cli_handlers.go). Both
 *  fields are optional: `kiro_cli` is omitted when the `--version` probe fails
 *  or times out, which is a normal state while the install is still running. */
interface VersionPayload {
  vibekit?: string;
  kiro_cli?: string;
}

/** A version pair. `""` means "not known", never "absent" — a reader renders
 *  what it has and says nothing about what it does not. */
export interface Versions {
  readonly vibekit: string;
  readonly kiroCli: string;
}

const EMPTY: Versions = { vibekit: "", kiroCli: "" };

/** Strip the program name kiro-cli prints alongside its version.
 *
 *  The server hands over the RAW `--version` stdout (`internal/server`'s
 *  handleVersion trims it and nothing more), and kiro-cli answers
 *  `kiro-cli 2.20.2` — measured on the live install. Every consumer here already
 *  supplies that word: the status card's line reads `kiro-cli <build> ready` and
 *  Settings → About puts the value under a `kiro-cli` label, so passing the raw
 *  string through renders `kiro-cli kiro-cli 2.20.2 ready` and `kiro-cli:
 *  kiro-cli 2.20.2`. Normalised HERE rather than at each interpolation, because
 *  this module is the one owner of the payload and a per-consumer strip is the
 *  same rule written twice.
 *
 *  Only that exact leading token goes, so a build string that does not carry it
 *  (a fork, or a future `--version` that prints the bare number) survives
 *  untouched rather than being parsed for a version-shaped substring. */
function bareKiroVersion(raw: string): string {
  const trimmed = raw.trim();
  const rest = trimmed.startsWith("kiro-cli ") ? trimmed.slice("kiro-cli ".length) : trimmed;
  return rest.trim();
}

const versions: Signal<Versions> = signal<Versions>(EMPTY);

/** The pair, as a signal, so a reader inside an `effect` repaints when it lands. */
export function versionsSignal(): Signal<Versions> {
  return versions;
}

/** The pair, untracked. For a reader that is not an effect. */
export function getVersions(): Versions {
  return versions.peek();
}

let started = false;

/** Read the pair once and publish it. Idempotent: a second call is a no-op, so a
 *  caller that is unsure whether boot already ran it can call it freely.
 *
 *  Never throws and never reports: an unknown version is a missing suffix on one
 *  line, and `apiGet` already logs the failure centrally. */
export async function loadVersions(): Promise<void> {
  if (started) {
    return;
  }
  started = true;
  const v = await apiGet<VersionPayload>("/api/version");
  if (v === null) {
    return;
  }
  versions.value = {
    vibekit: (v.vibekit ?? "").trim(),
    kiroCli: bareKiroVersion(v.kiro_cli ?? ""),
  };
}

/** Reset for tests. Not part of the app's own lifecycle — the pair is read once
 *  per page load and a page load is the reset. */
export function _resetVersionsForTest(): void {
  started = false;
  versions.value = EMPTY;
}
