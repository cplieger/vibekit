import { get, getActiveId } from "./store.js";
import { admitLocation, settleDeepLinkedChat } from "./deep-link.js";
import { switchSession } from "./chat.js";
import {
  activateTab,
  filesTabForRoute,
  openTab,
  setSettingsTab,
  setGitTab,
  setDocsTab,
} from "./tabs.js";
import { replaceRoute } from "./router.js";
import type { RouteOrigin } from "./router.js";
import type { Route } from "./route-path.js";
import { openFile } from "./editor-openers.js";
import { pointFilesTab } from "./files.js";
import { normalizeDirPath } from "./files-shared.js";
import { forceSettingsTab } from "./settings-tabs.js";
import { flushURLHighlight } from "./settings-highlight.js";
import { forceGitTab } from "./git-tabs.js";

/** Apply a route. RESOLVES when the view it names is open, which is what lets the
 *  router hold its claim on the location for the whole application: the `run` and
 *  `subagent` arms reach their opener through a dynamic `import()`, and while that
 *  import is in flight the active row is still whatever the boot restored.
 *
 *  The arms whose opener is `openTab` deliberately do NOT return its chain: that is a
 *  server mutation bounded only by the API timeout, and awaiting it would hold
 *  `markBootDone` and the identity region for that long on every deep-linked boot.
 *  They keep a narrower version of the same exposure — see the report. */
export function applyRoute(route: Route, origin: RouteOrigin = "deeplink"): Promise<void> {
  // Asked FIRST, because every branch below is an opener and from a Route alone they
  // cannot be told apart. `deep-link.ts` owns what a location is allowed to mean.
  if (admitLocation(route, origin) === "canonicalized") {
    return Promise.resolve();
  }
  switch (route.kind) {
    case "chat":
      if (route.id !== "" && get(route.id) !== undefined) {
        // The chat EXISTS, so `switchSession` either activates its tab or OPENS one.
        // Voided: a refusal has already raised its own notice through `openTabCommand`.
        void switchSession(route.id);
      } else if (route.id !== "") {
        // The id names NO ROW. Everything that decision needs — whether asking the server
        // can be answered at all, what its answer licenses, whether a verdict that arrived
        // a round trip late still describes the screen — lives in `deep-link.ts`. None of
        // it is routing. Voided: every outcome is returned rather than thrown, and the
        // module raises whatever notice its own evidence licenses.
        void settleDeepLinkedChat(route.id);
      } else if (getActiveId() !== "") {
        replaceRoute({ kind: "chat", id: getActiveId() });
      }
      break;
    // The FOUR singleton routes open their tab and then CORRECT its sub-tab, which a
    // subject cannot carry (its `ref` is empty). Each goes through `openTab` and NONE
    // through a `toggle*View` helper: a toggle CLOSES an already-active tab, so a
    // router that toggled would DESTROY the tab the URL names.
    case "settings":
      forceSettingsTab(route.tab);
      void openTab({ kind: "settings" }).then(() => {
        setSettingsTab(route.tab);
        // A `?highlight=` fires after the panel's loader, so the control it names exists by
        // the time we look for it. One-shot, so a later popstate does not re-flash it.
        flushURLHighlight();
      });
      break;
    case "git": {
      // Hoisted rather than read inside the callback: TypeScript discards the narrowing
      // of a property access across a function boundary, so `route.pr` re-widens to
      // `string | undefined` at a call inside the `.then()` and `requestPRFocus(identity:
      // string)` rejects it under `strict`.
      const pr = route.tab === "prs" ? (route.pr ?? "") : "";
      forceGitTab(route.tab);
      void openTab({ kind: "git" }).then(() => {
        setGitTab(route.tab);
        if (pr !== "") {
          // Dynamic, like the `docs` and `run` arms: the PRs tab must not join the
          // boot bundle.
          void import("./git-prs-tab.js").then(({ requestPRFocus }) => {
            requestPRFocus(pr);
          });
        }
      });
      break;
    }
    case "files": {
      const dir = normalizeDirPath(route.path);
      // A legacy or `.`-spelled path resolves to the same folder as its canonical form,
      // so the address bar is corrected BEFORE anything reads it. A replace rather than a
      // push, or the activation's own push stacks an entry that renders identically.
      replaceRoute({ kind: "files", path: dir });
      // A files route names a FOLDER rather than a tab, so it RE-POINTS an open browser
      // and mints only when none is open: a pasted /files/config moves the browser the
      // reader has, and middle-click is the door that asks for a second one.
      const { id, ref } = filesTabForRoute(dir);
      if (id === "") {
        void openTab({ kind: "files", ref: dir });
        break;
      }
      // `activateTab` rather than `openTab`: activating an already-open tab is a
      // LOCAL move, where openTab would spend a POST /api/command round trip per
      // Back press for a mutation the server answers created:false to. The
      // already-active case early-returns inside activateTabQuietly, which is
      // exactly why the load lives in pointFilesTab rather than in `refresh`.
      pointFilesTab(ref, dir);
      activateTab(id);
      break;
    }
    case "file":
      openFile(route.path, route.line);
      break;
    case "docs":
      // The sub-tab is forced BEFORE the open, matching its settings and git siblings:
      // this tab's refresh loads the ACTIVE panel, so forcing afterwards fetched
      // Steering and then painted Hooks. Reached through the lazy import the factory
      // already uses, which is what keeps the page out of the boot bundle.
      return import("./docs.js")
        .then(({ forceDocsTab }) => {
          forceDocsTab(route.tab);
          void openTab({ kind: "docs" }).then(() => {
            setDocsTab(route.tab);
          });
        })
        .catch(() => {
          /* noop */
        });
    case "history":
      void openTab({ kind: "history" });
      break;
    case "run":
      // RETURNED rather than voided: the router's claim on this location stands until it
      // resolves, so no unrelated projection emit can write the URL while the chunk loads.
      return import("./run-view.js")
        .then(async ({ openRunView }) => {
          // Deep link: the run's name is not in the URL, so the tab is titled by id until
          // the fetch supplies the real name. It still nests under the launching chat when
          // this client knows which one it was.
          //
          // The fourth argument is what makes a COPIED STEP LINK land on the step: the run
          // card's row href carries the node as `#node=<path>`. `""` means "the run" and
          // lets the page auto-follow.
          // AWAITED, not voided: the claim comes off when this promise settles, and the
          // open is a server round trip.
          await openRunView(route.id, route.id, "", route.node ?? "");
        })
        .catch(() => {
          /* noop */
        });
    case "subagent":
      // A delegate's page has nothing to fetch — its blocks are already in the chat store,
      // or they are not resident and the page says so — so this is just the tab.
      return import("./subagent-view.js")
        .then(async ({ openSubagentView }) => {
          // AWAITED for the reason the run branch above is: the claim on this location is
          // released when this promise settles, so releasing it before the tab exists lets
          // an unrelated emit write the restored tab's route over the reader's URL.
          await openSubagentView(route.chat, route.id);
        })
        .catch(() => {
          /* noop */
        });
  }
  return Promise.resolve();
}
