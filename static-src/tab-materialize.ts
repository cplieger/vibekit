// ---------------------------------------------------------------------------
// tab-materialize: the ONE place a TabSubject becomes a TabViewSpec.
//
// `materializeTab` is TOTAL over the eight tab kinds with no default branch, so
// a ninth kind added to the Go const block is a compile error here rather than a
// switch with no case for it on every connected device at once, already
// persisted. That totality is the whole point: a subject arrives from the server
// (a snapshot, an event, another device's open) and the strip has to be able to
// render it without knowing which door opened it.
//
// One answer per kind, not one per door. Today the same singleton gets
// materially different specs depending on who opened it — /git reached from a
// path link carries no onShow while the sidebar's door passes loadGitRepos, and a
// boot-restored Files tab carries no onClose while the sidebar's carries
// resetFileBrowser. A tab's behaviour cannot depend on who opened it, so this
// file takes the UNION of what the doors do, and each divergence is named at its
// case.
//
// TWO THINGS A SUBJECT CANNOT SAY, both stated rather than papered over:
//
//   1. The display NAME. A chat's name lives in the chat store and a run's in
//      the run store, so both are derived here — but a chat resumed from History
//      has no store row yet and a run has no state until its first inspect, and
//      in both cases today's opener passes a name from a source this module
//      cannot see (KAS's session-row title, a `run_started` payload's name, a
//      recipe's name). The factory falls back to a placeholder; a caller holding
//      a better name overrides the one field.
//   2. A run's PARENTLESSNESS. `showRun`'s third argument gates the retry
//      control, and it asks whether the RUN has a parent agent session — not
//      whether this tab nests under an open one. `TabSubject.Parent` answers the
//      second question only: a chat-parented run reviewed while its chat's tab is
//      closed has an empty Parent and is not parentless. So the injected opener
//      takes `(workflowID, owns)` and the wiring resolves parentlessness from the
//      run's own record.
//
// INJECTION, not import, for chat / editor / run. Those three behaviours live in
// modules that will themselves call this factory, so a static import of chat.ts
// or editor-core.ts from here closes a cycle. The composition root registers
// them, exactly the way tabs.ts already takes setReorderCallback. The five
// singletons need no injection: their loaders are reached through a LAZY import,
// which is what the design's "a singleton's lazy import stays in its factory"
// means and what app.ts already does for docs and history.
//
// DOM-free, and it reads only the two leaf stores (store.ts, run-store.ts) for
// names. Both reads are untracked by construction — `store.get` is a peek and
// `peekRunState` says so in its name — so materializing inside an effect cannot
// subscribe the caller to every chat and every run.
// ---------------------------------------------------------------------------

import type { TabKind, TabSubject } from "./types.js";
import { TAB_ICONS, TAB_VIEWS, type TabDotStatus, type TabViewSpec } from "./tab-view.js";
import { get } from "./store.js";
import { peekRunState } from "./run-store.js";

// --- The injected half ---

/** Chat behaviour, from chat.ts. `dot` is injected rather than derived here
 *  because the pending-ask half of it reads the decision dock, which is not a
 *  leaf module. */
export interface ChatTabOpener {
  show: (chatID: string) => void;
  close: (chatID: string, opts: { remote: boolean }) => void;
  dot: (chatID: string) => TabDotStatus | "";
}

/** Editor behaviour, from editor-openers.ts. The editor's loaded content, dirty
 *  state, mode and line selection stay in `fileStates`, so a subject needs to
 *  carry nothing but the path. */
export interface EditorTabOpener {
  show: (path: string) => void;
  close: (path: string) => void;
}

/** Run behaviour, from run-view.ts and actions/runs.ts.
 *
 *  `show` takes `owns` because that is the one authority fact the subject holds:
 *  an owned tab carries the status's live verbs and a review carries none. It
 *  deliberately does NOT take parentlessness — see this file's header for why the
 *  subject cannot answer that. */
export interface RunTabOpener {
  show: (workflowID: string, owns: boolean) => void;
  cancel: (workflowID: string) => void;
}

export interface TabOpeners {
  readonly chat: ChatTabOpener;
  readonly editor: EditorTabOpener;
  readonly run: RunTabOpener;
}

let openers: TabOpeners | null = null;

/** Register the behaviours the factory cannot author. Called once, from the
 *  composition root. Last registration wins, like setReorderCallback. */
export function registerTabOpeners(next: TabOpeners): void {
  openers = next;
}

/** The registered openers, or a loud failure.
 *
 *  It throws for EVERY kind, including the five singletons that need no
 *  injection, and that is deliberate: an unwired composition root then fails on
 *  the first tab it materializes rather than working through boot and blowing up
 *  when someone opens a chat. The alternative — checking only where an opener is
 *  read — is the half-working factory this check exists to prevent, and a spec
 *  whose `onShow` silently does nothing is exactly the failure that has no
 *  symptom until a reader clicks a tab and nothing loads. */
function requireOpeners(kind: TabKind): TabOpeners {
  if (openers === null) {
    throw new Error(
      `tab-materialize: no openers registered while materializing a "${kind}" tab; ` +
        `the composition root must call registerTabOpeners before any tab is materialized`,
    );
  }
  return openers;
}

/** Drop the registration. Test isolation only; production never unregisters. */
export function _resetTabOpenersForTest(): void {
  openers = null;
}

// --- Fallback labels ---

/** The name a chat with no store row reads as.
 *
 *  Byte-identical to chat.ts's own NEW_CHAT_NAME, and duplicated rather than
 *  shared because chat.ts is on the INJECTED side of this seam: importing it for
 *  one string would close the cycle this module exists to avoid. The following
 *  stage should move the constant to a leaf both sides can read. */
const FALLBACK_CHAT_NAME = "New conversation";

/** The name a run this client has fetched nothing for reads as. Matches
 *  handlers/run.ts's `runLabel` fallback, which is what a toast for the same
 *  nameless run already says. */
const FALLBACK_RUN_NAME = "Workflow run";

/** A chat's label, from the chat store.
 *
 *  `store.get` is an untracked peek (the collection's own contract), so this is
 *  safe to call from inside an effect. An empty stored name is the same case as
 *  no row at all: every existing caller writes the fallback for it. */
function chatName(chatID: string): string {
  const name = get(chatID)?.name ?? "";
  return name === "" ? FALLBACK_CHAT_NAME : name;
}

/** A run's label. `runLabel` is what the launcher called this EXECUTION and
 *  `workflowName` the recipe's own name, in that order, which is the preference
 *  the transcript's run card already applies. */
function runName(workflowID: string): string {
  const state = peekRunState(workflowID);
  const label = state?.runLabel ?? "";
  if (label !== "") {
    return label;
  }
  const recipe = state?.workflowName ?? "";
  return recipe === "" ? FALLBACK_RUN_NAME : recipe;
}

/** A file's tab label: its last path segment. Identical to what openEditorView
 *  computes today, deliberately, so a converted call site renames nothing. */
function fileName(path: string): string {
  return path.split("/").pop() ?? path;
}

// --- Pass-through subject facts ---

/** The sub-tab position, as the store spells it.
 *
 *  A subject says "no parent" with an EMPTY STRING and the store says it with an
 *  ABSENT field, so the translation happens once, here. Both mean the same thing
 *  to the two sides that read them: `insertRow` promotes an orphan to top level
 *  and so does the server's Open, for the same reason — a tab nobody can see is
 *  worse than a tab in the wrong place. */
function parentOf(subject: TabSubject): { parentId?: string } {
  return subject.parent === "" ? {} : { parentId: subject.parent };
}

/** The dot, as the store spells it. `""` means "nothing painted", which the spec
 *  represents as an ABSENT field so a reader can tell it from "painted, then
 *  cleared". */
function dotOf(status: TabDotStatus | ""): { dotStatus?: TabDotStatus } {
  return status === "" ? {} : { dotStatus: status };
}

/** Run a singleton's loader through a LAZY import, swallowing a failed chunk
 *  load the way app.ts already does: the tab is open and visible, and there is
 *  nothing useful to tell a reader about a chunk that did not arrive.
 *
 *  Every singleton loader is reached this way, including the three app.ts imports
 *  statically today, and the reason is the same seam: settings-tabs.ts, git.ts
 *  and files.ts all reach tabs.ts, and the following stage puts a materializeTab
 *  call inside tabs.ts's own toggle helpers, so a static import here would close
 *  a cycle exactly as a static import of chat.ts would. The import specifier stays
 *  written out at each call site because a bundler resolves a literal, not an
 *  expression. */
function lazily(load: Promise<unknown>): void {
  void load.catch(() => {
    /* noop */
  });
}

// --- The factory ---

/** Produce the local half of a tab from the shared half.
 *
 *  Exhaustive over TabKind with NO default branch: every case returns, so a
 *  ninth kind makes the function fall off its end and `strictNullChecks` rejects
 *  it. Do not add a default — it would turn that compile error into a runtime
 *  one on every connected device.
 *
 *  Never calls a toggle-style opener. A factory that toggles is not a factory:
 *  `toggleSettingsView` and its four siblings CLOSE the tab when it is already
 *  active, so reaching one from here would make materializing a subject destroy
 *  the tab it describes. The loaders below are the plain LOADER half of those
 *  doors, which is the same rule app.ts's restore already follows. */
export function materializeTab(subject: TabSubject): TabViewSpec {
  const reg = requireOpeners(subject.kind);
  switch (subject.kind) {
    case "chat": {
      const chatID = subject.ref;
      return {
        name: chatName(chatID),
        icon: TAB_ICONS.chat,
        view: TAB_VIEWS.chat,
        route: { kind: "chat", id: chatID },
        // From the SUBJECT. A side conversation's own subject carries owns:true
        // because it owns its bridge; a tab that only WATCHES another chat's work
        // carries owns:false. Neither is a property of the kind.
        owns: subject.owns,
        ...parentOf(subject),
        ...dotOf(reg.chat.dot(chatID)),
        onShow: () => {
          reg.chat.show(chatID);
        },
        // The whole-argument default matches the store's contract: a caller that
        // omits the flag means LOCAL, which is the safe reading, since a missing
        // flag must never suppress the server-side teardown.
        onClose: ({ remote } = { remote: false }) => {
          reg.chat.close(chatID, { remote });
        },
      };
    }
    case "editor": {
      const path = subject.ref;
      return {
        name: fileName(path),
        icon: TAB_ICONS.editor,
        view: TAB_VIEWS.editor,
        // No line, and no mode. A `#L<line>` fragment and the edit/diff/image
        // mode are the OPENER's arguments, not facts about what is open, so they
        // stay where they are today: the mode in `fileStates`, the line in the
        // pushRoute the opener issues after this. Matches openEditorView's route
        // exactly.
        route: { kind: "file", path },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          reg.editor.show(path);
        },
        onClose: () => {
          reg.editor.close(path);
        },
      };
    }
    case "run": {
      const workflowID = subject.ref;
      // Read once and used twice, because the two uses are the same fact: the
      // controls the view offers, and whether the × cancels.
      const owns = subject.owns;
      return {
        name: runName(workflowID),
        icon: TAB_ICONS.run,
        view: TAB_VIEWS.run,
        route: { kind: "run", id: workflowID },
        owns,
        ...parentOf(subject),
        onShow: () => {
          reg.run.show(workflowID, owns);
        },
        // A REVIEW gets no onClose at all rather than one the store would skip.
        // closeTab already refuses to fire onClose when owns is false, so either
        // spelling behaves the same — and the absent one says the honest thing:
        // dismissing a view has nothing to tear down. An OWNED run's × means
        // stop, because a workflow that outlived its only surface would spend
        // credits and edit files with nothing on screen.
        ...(owns
          ? {
              onClose: (): void => {
                reg.run.cancel(workflowID);
              },
            }
          : {}),
      };
    }
    case "settings":
      return {
        name: "Settings",
        icon: TAB_ICONS.settings,
        view: TAB_VIEWS.settings,
        // The CANONICAL sub-tab, because a subject cannot carry one: a
        // singleton's Ref is empty. The actual sub-tab is corrected afterwards by
        // setSettingsTab / applyRoute, which is how the boot restore already
        // works.
        route: { kind: "settings", tab: "general" },
        owns: subject.owns,
        ...parentOf(subject),
        // DIVERGENCE the union resolves: three of this tab's doors
        // (recipes.ts, settings-highlight.ts, settings.ts) pass no onShow at all,
        // so activating a Settings tab they opened loads nothing. The restore
        // door loads. One tab, one behaviour.
        onShow: () => {
          lazily(
            import("./settings-tabs.js").then(({ loadSettingsTabData }) => {
              loadSettingsTabData("general");
            }),
          );
        },
      };
    case "git":
      return {
        name: "Source Control",
        icon: TAB_ICONS.git,
        view: TAB_VIEWS.git,
        route: { kind: "git", tab: "changes" },
        owns: subject.owns,
        ...parentOf(subject),
        // Same divergence as settings: navigate.ts's path-link door passes no
        // onShow, so /git reached from a chat's file link did not refresh its
        // repos while the sidebar's door did.
        onShow: () => {
          lazily(
            import("./git.js").then(({ loadGitRepos }) => {
              loadGitRepos();
            }),
          );
        },
      };
    case "files":
      return {
        name: "Files",
        icon: TAB_ICONS.files,
        view: TAB_VIEWS.files,
        route: { kind: "files", path: "." },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          lazily(
            import("./files.js").then(({ loadFileBrowser }) => {
              loadFileBrowser();
            }),
          );
        },
        // DIVERGENCE the union resolves the other way: the sidebar's door passes
        // resetFileBrowser and the boot restore passes nothing, so a restored
        // Files tab kept its stale rows and its search mode when closed. The
        // reset is the intended behaviour of a files-tab close, so every door
        // gets it.
        onClose: () => {
          lazily(
            import("./files.js").then(({ resetFileBrowser }) => {
              resetFileBrowser();
            }),
          );
        },
      };
    case "history":
      return {
        name: "History",
        icon: TAB_ICONS.history,
        view: TAB_VIEWS.history,
        route: { kind: "history" },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          lazily(
            import("./history.js").then(({ loadHistoryView }) => {
              loadHistoryView();
            }),
          );
        },
        // Unlike docs, this page needs a close hook: it holds a dispatch, an
        // AbortController and a debounce timer.
        onClose: () => {
          lazily(
            import("./history.js").then(({ teardownHistoryView }) => {
              teardownHistoryView();
            }),
          );
        },
      };
    case "docs":
      return {
        name: "Kiro docs",
        icon: TAB_ICONS.docs,
        view: TAB_VIEWS.docs,
        route: { kind: "docs", tab: "steering" },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          lazily(
            import("./docs.js").then(({ loadDocsView }) => {
              loadDocsView("steering");
            }),
          );
        },
      };
  }
}
