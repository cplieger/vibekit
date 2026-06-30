// ---------------------------------------------------------------------------
// Client-side URL router: maps browser URL to the active view/tab.
//
// URL scheme:
//   /                             default chat (last active, or empty state)
//   /chat/{id}                    specific conversation
//   /git                          git panel (Changes tab — canonical)
//   /git/{tab}                    git panel sub-tab (prs | sources; changes omits the segment)
//   /files[/{path}]               file browser at path (omit for workspace root)
//   /file/{path}                  file editor for a specific file, with optional #L<line>
//   /history                      archived chats (full-page view)
//   /settings                     Settings (General tab)
//   /settings/tools               Settings → Tools
//   /settings/permissions         Settings → Permissions
//   /settings/instructions        Settings → Custom Instructions
//   /settings/git                 Settings → Git & forges
//
// Shell, popups, modals, and the model-switch affordance are transient UI;
// they don't get URLs.
//
// Canonical forms: `/settings` is preferred over `/settings/general` (both
// parse to the same route; pushRoute writes the shorter form back).
// ---------------------------------------------------------------------------

// --- Route types ---

export type SettingsTab = "general" | "tools" | "permissions" | "instructions" | "git";

// The git view's three sub-tabs. "changes" is the canonical default (its URL
// omits the segment: /git, not /git/changes), mirroring how SettingsTab's
// "general" maps to /settings.
export type GitTab = "changes" | "prs" | "sources";

interface RouteChat {
  kind: "chat";
  id: string;
}
interface RouteGit {
  kind: "git";
  tab: GitTab;
}
interface RouteFiles {
  kind: "files";
  path: string;
}
interface RouteFile {
  kind: "file";
  path: string;
  line?: number;
}
interface RouteHistory {
  kind: "history";
}
interface RouteSettings {
  kind: "settings";
  tab: SettingsTab;
}

export type Route = RouteChat | RouteGit | RouteFiles | RouteFile | RouteHistory | RouteSettings;

// --- Parse current URL into a Route ---

/** Wrapper around decodeURIComponent that returns the raw input on
 *  malformed percent-encoded sequences instead of throwing. Browsers
 *  can navigate to URLs with bare `%` characters (e.g. pasted from
 *  external tools), and popstate fires with the raw pathname. */
function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function parseRoute(pathname: string, hash: string = location.hash): Route {
  // Normalise: strip leading/trailing slashes for clean segment splitting.
  const segments = pathname.replace(/^\/+|\/+$/g, "").split("/");
  const head = segments[0] ?? "";

  switch (head) {
    case "git":
      return { kind: "git", tab: parseGitTab(segments[1]) };

    case "history":
      return { kind: "history" };

    case "settings":
      return { kind: "settings", tab: parseSettingsTab(segments[1]) };

    case "chat": {
      const id = safeDecode(segments[1] ?? "");
      if (id !== "") {
        return { kind: "chat", id };
      }
      break;
    }

    case "files": {
      // /files → workspace root; /files/<path> → specific path.
      if (segments.length <= 1) {
        return { kind: "files", path: "." };
      }
      const filePath = safeDecode(segments.slice(1).join("/"));
      return { kind: "files", path: filePath === "" ? "." : filePath };
    }

    case "file": {
      if (segments.length <= 1) {
        break;
      } // missing path → fall through to default
      const filePath = safeDecode(segments.slice(1).join("/"));
      if (filePath === "") {
        break;
      }
      const line = parseHashLine(hash);
      return line !== undefined
        ? { kind: "file", path: filePath, line }
        : { kind: "file", path: filePath };
    }
  }

  // Default: chat with no specific ID (last active, or empty state).
  return { kind: "chat", id: "" };
}

// parseSettingsTab normalises an unknown / missing tab segment to "general"
// so bogus URLs still land somewhere useful instead of 404-ing.
function parseSettingsTab(seg: string | undefined): SettingsTab {
  switch (seg) {
    case "tools":
    case "permissions":
    case "instructions":
    case "git":
      return seg;
    default:
      return "general";
  }
}

// parseGitTab normalises an unknown / missing sub-tab segment to "changes"
// (the canonical default), mirroring parseSettingsTab.
function parseGitTab(seg: string | undefined): GitTab {
  switch (seg) {
    case "prs":
    case "sources":
      return seg;
    default:
      return "changes";
  }
}

function parseHashLine(hash: string): number | undefined {
  const m = /^#L(\d+)/.exec(hash);
  if (m === null) {
    return undefined;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const n = parseInt(m[1]!, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

// --- Build a URL path from a Route ---

export function buildPath(route: Route): string {
  switch (route.kind) {
    case "chat":
      return route.id === "" ? "/" : `/chat/${encodeURIComponent(route.id)}`;
    case "git":
      // Changes is the canonical default; omit the tab segment.
      return route.tab === "changes" ? "/git" : `/git/${route.tab}`;
    case "history":
      return "/history";
    case "files":
      return route.path === "." || route.path === ""
        ? "/files"
        : `/files/${encodePath(route.path)}`;
    case "file":
      return route.line !== undefined && route.line > 0
        ? `/file/${encodePath(route.path)}#L${String(route.line)}`
        : `/file/${encodePath(route.path)}`;
    case "settings":
      // General is the canonical default; omit the tab segment.
      return route.tab === "general" ? "/settings" : `/settings/${route.tab}`;
  }
}

// encodePath URL-encodes each path segment while preserving the separators,
// so a file at "dir/my file.md" serialises to "dir/my%20file.md" rather
// than collapsing to an unreadable blob.
function encodePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

// --- Push or replace the URL without triggering popstate ---

let suppressed = false;
export function suppressPush(v: boolean): void {
  suppressed = v;
}

export function pushRoute(route: Route): void {
  if (suppressed) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target !== current) {
    history.pushState(null, "", target);
  }
}

export function replaceRoute(route: Route): void {
  if (suppressed) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target !== current) {
    history.replaceState(null, "", target);
  }
}

// --- Listen for back/forward navigation ---

let popstateHandler: ((route: Route) => void) | undefined;

export function onPopState(handler: (route: Route) => void): void {
  popstateHandler = handler;
}

// Cache the last parsed route to skip redundant URL parsing on rapid popstate.
let cachedKey = "";
let cachedRoute: Route | undefined;

window.addEventListener("popstate", () => {
  if (popstateHandler !== undefined) {
    const key = location.pathname + location.hash;
    if (key === cachedKey && cachedRoute !== undefined) {
      popstateHandler(cachedRoute);
      return;
    }
    const route = parseRoute(location.pathname, location.hash);
    cachedKey = key;
    cachedRoute = route;
    popstateHandler(route);
  }
});
