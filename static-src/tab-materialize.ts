// ---------------------------------------------------------------------------
// tab-materialize: the ONE place a TabSubject becomes a TabViewSpec.
//
// `materializeTab` is TOTAL over the nine tab kinds with no default branch, so
// a tenth kind added to the Go const block is a compile error here rather than a
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
//   1. The display NAME. A chat's name lives in the chat store, a run's in the
//      run store, and a subagent's in the invocation tool call inside its chat's
//      messages, so all three are derived here — but a chat resumed from History
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
// `runLabelOf` documents itself as one — so materializing inside an effect cannot
// subscribe the caller to every chat and every run.
// ---------------------------------------------------------------------------

import type { TabKind, TabSubject } from "./types.js";
import type { Route } from "./router.js";
import { TAB_ICONS, TAB_VIEWS, type TabDotStatus, type TabViewSpec } from "./tab-view.js";
import { get } from "./store.js";
import { runLabelOf } from "./run-store.js";
import { FALLBACK_SUBAGENT_NAME, subagentLabel } from "./roles.js";
import { findSubagentInvocation } from "./subagent-slice.js";

// --- The injected half ---

/** Chat behaviour, from chat.ts. `dot` is injected rather than derived here
 *  because the pending-ask half of it reads the decision dock, which is not a
 *  leaf module. `close` is the chat's CLIENT-LOCAL teardown, whoever closed the
 *  tab — the server's close operation owns everything beyond this device. */
export interface ChatTabOpener {
  show: (chatID: string) => void;
  close: (chatID: string) => void;
  dot: (chatID: string) => TabDotStatus | "";
}

/** Editor behaviour, from editor-openers.ts. The editor's loaded content, dirty
 *  state, mode and line selection stay in `fileStates`, so a subject needs to
 *  carry nothing but the path. */
export interface EditorTabOpener {
  show: (path: string) => void;
  close: (path: string) => void;
}

/** Run behaviour, from run-view.ts.
 *
 *  `show` takes the workflow id and nothing else. It used to take `owns`, because an
 *  owned tab carried the status's live verbs and a review carried none — both halves
 *  of that are gone: a run tab is always a VIEW now, and the control row is offered
 *  wherever the run is stoppable rather than by which door opened the tab. There is
 *  no `cancel` either; nothing closing a tab cancels a run. */
export interface RunTabOpener {
  show: (workflowID: string) => void;
}

/** Subagent behaviour, from subagent-view.ts.
 *
 *  There is no `close` half, and its absence is the design rather than a gap. A
 *  subagent tab is a READING SURFACE over blocks that live in the chat store: it
 *  owns nothing, starts nothing and can stop nothing, so closing it has nothing
 *  to tear down. Every such tab is opened with `owns: false` for the same reason,
 *  which is what makes the missing hook unreachable rather than merely unused. */
export interface SubagentTabOpener {
  show: (chatID: string, subtaskID: string) => void;
}

export interface TabOpeners {
  readonly chat: ChatTabOpener;
  readonly editor: EditorTabOpener;
  readonly run: RunTabOpener;
  readonly subagent: SubagentTabOpener;
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
  const label = runLabelOf(workflowID);
  return label === "" ? FALLBACK_RUN_NAME : label;
}

/** A file's tab label: its last path segment. Identical to what openEditorView
 *  computes today, deliberately, so a converted call site renames nothing. */
function fileName(path: string): string {
  return path.split("/").pop() ?? path;
}

// --- The subagent ref codec ---

/** The one composite ref on this wire: `<chatID>/<agentSubtaskID>`.
 *
 *  It has to be composite because nothing indexes a subtask id to a chat. A run
 *  gets away with a bare id because `GET /api/runs/{id}` resolves one cold; a
 *  delegate has no endpoint and no cross-chat index, so its blocks are only
 *  findable through the chat that holds them.
 *
 *  A slash separator is safe rather than convenient: `ids.ValidChatID` admits no
 *  slash, and the command boundary validates a chat ref with it, so the FIRST
 *  slash is always the seam even when a subtask id carries more (a workflow
 *  step's `wf:<id>:<a/b>` shape does, though a step never reaches this kind).
 *
 *  The codec lives here because this is the module that reads and writes what
 *  every ref MEANS — the factory in one direction, `subjectForRoute` in the
 *  other — so a second spelling elsewhere could disagree with it. */
export function subagentRef(chatID: string, subtaskID: string): string {
  return `${chatID}/${subtaskID}`;
}

/** Split a subagent ref back into its two halves. Both empty for a malformed
 *  ref, which the factory renders as a page that says it cannot find the
 *  delegate rather than as a thrown error on someone else's device: a ref
 *  arrives from the persisted set, so a bad one has to be survivable. */
export function parseSubagentRef(ref: string): { chatID: string; subtaskID: string } {
  const cut = ref.indexOf("/");
  if (cut <= 0 || cut === ref.length - 1) {
    return { chatID: "", subtaskID: "" };
  }
  return { chatID: ref.slice(0, cut), subtaskID: ref.slice(cut + 1) };
}

/** A delegate's label, from the chat store's own record of its invocation.
 *
 *  Derived rather than carried, so a tab RESTORED on boot reads the same as one
 *  the transcript's link opened. The scan is over one chat's resident messages
 *  and runs once per materialization, not per render; a chat whose page has not
 *  been fetched yet has no invocation to find and falls back, and the next
 *  materialization (or the opener's own name) corrects it. */
function subagentTabName(ref: string): string {
  const { chatID, subtaskID } = parseSubagentRef(ref);
  if (chatID === "") {
    return FALLBACK_SUBAGENT_NAME;
  }
  const tc = findSubagentInvocation(get(chatID)?.messages ?? [], subtaskID);
  return tc === undefined ? FALLBACK_SUBAGENT_NAME : subagentLabel(tc);
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
 *  tenth kind makes the function fall off its end and `strictNullChecks` rejects
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
        onClose: () => {
          reg.chat.close(chatID);
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
      // A RUN TAB IS ALWAYS A VIEW: `owns: false`, no `onClose`, so dismissing it
      // stops nothing (user decision, 2026-08, superseding the earlier split where
      // the launcher's own tab cancelled on ×).
      //
      // The subpage view is universal — one component serves a workflow run and a
      // subagent — and a × that means "close this" on one door and "destroy the
      // work" on another is a gesture a reader cannot learn. It was also the only
      // destructive control in the app with no confirmation, reachable by the
      // smallest target on the row.
      //
      // The consequence is accepted rather than overlooked: a parentless run can now
      // outlive every view of it. Stopping one is the CANCEL VERB, which is why the
      // control row is no longer gated on which door opened the tab (run-view.ts) —
      // with the × disarmed, gating Cancel would have made a live run reachable and
      // unstoppable. The launching chat's × still cancels its runs, and that is a
      // different gesture: it destroys the conversation, not a view of it.
      return {
        name: runName(workflowID),
        icon: TAB_ICONS.run,
        view: TAB_VIEWS.run,
        route: { kind: "run", id: workflowID },
        owns: false,
        ...parentOf(subject),
        onShow: () => {
          reg.run.show(workflowID);
        },
      };
    }
    case "subagent": {
      const { chatID, subtaskID } = parseSubagentRef(subject.ref);
      return {
        name: subagentTabName(subject.ref),
        icon: TAB_ICONS.subagent,
        view: TAB_VIEWS.subagent,
        route: { kind: "subagent", chat: chatID, id: subtaskID },
        owns: subject.owns,
        ...parentOf(subject),
        onShow: () => {
          reg.subagent.show(chatID, subtaskID);
        },
        // No onClose, and unlike the run REVIEW's absence this one is
        // unconditional: the page is a projection of blocks the chat store owns,
        // so there is nothing a close could stop. Every door opens it with
        // `owns: false`, which is what makes an owned subagent tab
        // unrepresentable rather than merely unhandled.
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
        name: "Git",
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

// --- The inverse ---

/** The subject a URL route names: which tab kind, and which ref.
 *
 *  The exact inverse of the `route` each case above produces, and it lives beside
 *  them for that reason — a new kind is ONE compile error covering both
 *  directions rather than two files that can disagree about what `/run/{id}`
 *  means. Total over the nine route kinds with no default branch, same rule as
 *  the factory.
 *
 *  A singleton's route carries a sub-position (a settings tab, a browser path)
 *  and its subject carries none, so that half is dropped: `/settings/tools` and
 *  `/settings` name the same tab. That asymmetry is the design — the sub-position
 *  is corrected AFTER the tab is activated, by applyRoute — so this direction
 *  round-trips and the other deliberately does not.
 *
 *  Its consumer is app.ts's back/forward guard. A history entry may only ACTIVATE
 *  a tab that is already open, so the projection is asked whether this route names
 *  one before the route is applied at all. Without that question a back press onto
 *  a closed tab's URL OPENED a fresh tab at it: the reader watched a tab they had
 *  closed come back, and under the server-owned collection every other device had
 *  to absorb it too. */
export function subjectForRoute(route: Route): { kind: TabKind; ref: string } {
  switch (route.kind) {
    case "chat":
      return { kind: "chat", ref: route.id };
    // The one case where the two vocabularies differ: the route kind is `file`
    // and the tab kind is `editor`.
    case "file":
      return { kind: "editor", ref: route.path };
    case "run":
      return { kind: "run", ref: route.id };
    case "subagent":
      return { kind: "subagent", ref: subagentRef(route.chat, route.id) };
    case "settings":
      return { kind: "settings", ref: "" };
    case "git":
      return { kind: "git", ref: "" };
    case "files":
      return { kind: "files", ref: "" };
    case "history":
      return { kind: "history", ref: "" };
    case "docs":
      return { kind: "docs", ref: "" };
  }
}
