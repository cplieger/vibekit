// ---------------------------------------------------------------------------
// Client-side URL router: maps browser URL to the active view/tab.
//
// URL scheme:
//   /                             default chat (last active, or empty state)
//   /chat/{id}                    specific conversation
//   /chat/{id}/subagent/{taskId}  one subagent execution of that conversation
//   /git                          git panel (Changes tab — canonical)
//   /git/{tab}                    git panel sub-tab (prs | sources; changes omits the segment)
//   /files[/{path}]               file browser at path (omit for workspace root)
//   /file/{path}                  file editor for a specific file, with optional #L<line>
//   /docs                         Kiro configuration browser (Steering tab — canonical)
//   /docs/{tab}                   browser sub-tab (skills | agents | specs | hooks | workflows)
//   /history                      previous chats + workflow runs (full-page view)
//   /run/{workflowId}             one workflow run (a review, or a launcher-owned live tab)
//   /settings                     Settings (General tab)
//   /settings/tools               Settings → Tools
//   /settings/permissions         Settings → Permissions
//   /settings/instructions        Settings → Custom Instructions
//
// Shell, popups, modals, and the model-switch affordance are transient UI;
// they don't get URLs.
//
// Canonical forms: `/settings` is preferred over `/settings/general` (both
// parse to the same route; pushRoute writes the shorter form back).
// ---------------------------------------------------------------------------

// --- Route types ---

// There is no "git" settings tab: the old "Git & forges" pane was retired with
// the multi-repo git-page rewrite (forge accounts live on the git view's
// Sources tab). /settings/git canonicalizes to General via parseSettingsTab's
// default branch.
export type SettingsTab = "general" | "tools" | "permissions" | "instructions";

// The git view's three sub-tabs. "changes" is the canonical default (its URL
// omits the segment: /git, not /git/changes), mirroring how SettingsTab's
// "general" maps to /settings.
export type GitTab = "changes" | "prs" | "sources";

// The configuration browser's six sub-tabs. "steering" is the canonical default
// and its URL omits the segment (/docs, not /docs/steering), mirroring
// SettingsTab's "general" and GitTab's "changes".
//
// "workflows" is RPC-sourced rather than a .kiro file scan (a recipe is compiled
// into KAS or lives under its sessions tree), which is why it arrived later than
// the rest — but it is a sub-tab like any other and MUST stay in parseDocsTab
// below. It was added here and to formatRoute without the parser, so the app
// wrote /docs/workflows and then read it back as /docs: a reload, a back button
// or a shared link silently landed on Steering.
export type DocsTab = "steering" | "skills" | "agents" | "specs" | "hooks" | "workflows";

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
/** The Kiro configuration browser. */
interface RouteDocs {
  kind: "docs";
  tab: DocsTab;
}
/** A read-only review of one previous workflow run. */
interface RouteRun {
  kind: "run";
  id: string;
}
/** One SUBAGENT execution, read on its own page.
 *
 *  Two fields, and the chat is not decoration. Nothing indexes an
 *  `agent_subtask_id` to a chat — there is no subagent endpoint and no cross-chat
 *  subtask index — so `/subagent/{id}` alone would be unresolvable on a cold
 *  load, unlike `/run/{id}`, which `GET /api/runs/{id}` answers from nothing. The
 *  path nests under the conversation for the same reason the tab nests under it:
 *  a delegate belongs to the turn that dispatched it. */
interface RouteSubagent {
  kind: "subagent";
  /** The chat whose transcript holds this delegate's blocks. */
  chat: string;
  /** The delegate's `agent_subtask_id`. */
  id: string;
}
interface RouteSettings {
  kind: "settings";
  tab: SettingsTab;
}

export type Route =
  | RouteChat
  | RouteGit
  | RouteFiles
  | RouteFile
  | RouteHistory
  | RouteDocs
  | RouteRun
  | RouteSubagent
  | RouteSettings;

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

    case "docs":
      return { kind: "docs", tab: parseDocsTab(segments[1]) };

    case "run": {
      const id = safeDecode(segments[1] ?? "");
      if (id !== "") {
        return { kind: "run", id };
      }
      break;
    }

    case "settings":
      return { kind: "settings", tab: parseSettingsTab(segments[1]) };

    case "chat": {
      const id = safeDecode(segments[1] ?? "");
      if (id === "") {
        break;
      }
      // /chat/{id}/subagent/{subtaskId} — a delegate of this conversation, read
      // on its own page. Checked before the plain chat route returns, because a
      // longer path under `chat` is a different location rather than a suffix to
      // ignore; an unrecognised third segment falls through to the chat itself,
      // which is the nearest thing that does exist.
      if (segments[2] === "subagent") {
        const subtask = safeDecode(segments.slice(3).join("/"));
        if (subtask !== "") {
          return { kind: "subagent", chat: id, id: subtask };
        }
      }
      return { kind: "chat", id };
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
      return seg;
    default:
      // Unknown segments include the retired "git" tab (/settings/git),
      // which had no panel or pill in the DOM — deep links land on General.
      return "general";
  }
}

// parseDocsTab normalises an unknown / missing sub-tab segment to "steering"
// (the canonical default), mirroring parseSettingsTab.
function parseDocsTab(seg: string | undefined): DocsTab {
  switch (seg) {
    case "skills":
    case "agents":
    case "specs":
    case "hooks":
    case "workflows":
      return seg;
    default:
      return "steering";
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
    case "docs":
      // Steering is the canonical default; omit the tab segment.
      return route.tab === "steering" ? "/docs" : `/docs/${route.tab}`;
    case "run":
      return `/run/${encodeURIComponent(route.id)}`;
    case "subagent":
      return `/chat/${encodeURIComponent(route.chat)}/subagent/${encodeURIComponent(route.id)}`;
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

/** How many callers are currently suppressing pushes.
 *
 *  A COUNT, not a flag, because the boot's regions run concurrently now: the
 *  settings restore and the tab restore each open a window, and with a boolean
 *  whichever closed first un-suppressed the other's — so a restore's own
 *  activation pushed a URL. Clamped at zero so an unbalanced `false` cannot leave
 *  the app permanently suppressed. */
let suppressDepth = 0;

export function suppressPush(v: boolean): void {
  suppressDepth = v ? suppressDepth + 1 : Math.max(0, suppressDepth - 1);
}

export function pushRoute(route: Route): void {
  if (suppressDepth > 0) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target !== current) {
    history.pushState(null, "", target);
  }
}

export function replaceRoute(route: Route): void {
  if (suppressDepth > 0) {
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
