// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns the
// chat-tab registration (openChatTab) and load-more pagination.

import type { ResumableSession } from "./types.js";
import {
  getActiveId,
  get,
  watchSession,
  setActive,
  setModel,
  upsertHeader,
  activeSession,
  removeChat,
  tabStatusFor,
  isEmptyChat,
  transcriptStale,
} from "./store.js";
import { loadList, loadMessages, confirmChatExists } from "./store-load.js";
import { effect, el } from "@cplieger/reactive";
import type { ChatHeader, Session } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  setTabTooltip,
  openChatRefs,
  type OpenTabOutcome,
  type TabDotStatus,
} from "./tabs.js";
import { beginAdopt, adoptCommitted, opFailed } from "./tabs-sync.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import {
  mountChatView,
  setLoadMore,
  loadTurnRail,
  pointTurnRail,
  fadeInTranscript,
  activeTranscriptView,
  transcriptViewFor,
  disposeChatView,
} from "./messages.js";
import { addAttachment } from "./attachments.js";
import {
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  seedComposerState,
  flushComposerDraft,
  dropComposerState,
} from "./composer-state.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { labelForMode } from "./roles.js";
import { refreshContextUI } from "./context-ui.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { info } from "./toast.js";
import { createChat, forkChat, setMode, type CreatedChat } from "./actions/chat.js";
import { newOpID } from "./transport.js";

// --- Bus: activate chat from other modules without importing chat.ts ---

onBus(BUS_ACTIVATE_CHAT, (p) => {
  activateChatView(p.chatID);
  if (p.then !== undefined) {
    p.then();
  }
});

// --- Chat tab registration ---

/** Open (or activate) a chat tab. Pass `{ activate: false }` for a bulk open:
 *  activation runs activateChatView (messages fetch + conflicts prefetch), so
 *  bulk-opening N chats active would fan out 2N requests.
 *
 *  A ROUND TRIP, resolving once the tab is in the projection: the tab set is
 *  server-owned, so `open_tab` creates it and `tabs_changed` paints it. */
export async function openChatTab(
  id: string,
  name: string,
  opts?: { activate?: boolean; parentTabID?: string },
): Promise<OpenTabOutcome> {
  return openTab({
    kind: "chat",
    ref: id,
    name,
    // `parentTabID`, not a chat id: `TabSubject.Parent` names an open TAB, and an
    // unresolvable parent silently promotes the tangent to top level. `owns` stays at
    // its default — a side chat owns its own bridge.
    ...(opts?.parentTabID === undefined ? {} : { parent: opts.parentTabID }),
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

/** A chat tab's dot state right now: the chat's live state, with a pending ask
 *  outranking it. Named because two producers need the same answer — this module's
 *  opener and tab-materialize.ts, which cannot read the decision dock itself. */
export function chatTabDot(id: string): TabDotStatus | "" {
  return tabStatusFor(get(id), hasPendingDecision(id));
}

/** Everything closing a chat TAB does on this device — client-local cleanup only,
 *  identical whoever closed the tab. Run DEFERRED for a close this device dispatched
 *  (exactly once per closed tab) and immediately for a remote close's applied
 *  removal. Nothing here dispatches: process teardown and the retention-off record
 *  delete are both the server's `close_tab`. */
export function closeChatTab(id: string): void {
  // The draft belongs to the CHAT, not the tab, so a pending save goes out before the
  // row drops.
  flushComposerDraft();
  // The dock queue is keyed by chat id, so a queue left behind was resurrected by
  // reopening the SAME id — a dot claiming a decision that no longer existed.
  dropDecisions(id);
  // Before removeChat: the store reassigning the active chat repaints synchronously,
  // and a dead view still in the registry would be parked by that paint.
  disposeChatView(id);
  // The store row, whatever retention says. Under retention the record survives
  // server-side; with retention off the server deleted it inside the close.
  removeChat(id);
  // Local only: reopening the chat seeds the text back from the server.
  dropComposerState(id);
  // `removeChat` reassigns the store's active chat with no activation behind it, so
  // without this retarget the next keystroke lands in no chat at all. Harmless when
  // the strip activates a neighbour straight after — a retarget is idempotent.
  retargetComposer(getActiveId());
}

// --- Activation generation ---

/** Bumped by every activateChatView call.
 *
 *  Two activations for one chat are ordinary, and store-load aborts the older fetch —
 *  without this guard the superseded activation paints its retry box over a transcript
 *  that just loaded fine. It completes the `getActiveId() !== id` check beside it,
 *  which only catches a newer activation of a DIFFERENT chat. */
let activationGen = 0;

/** Remove a previous activation's failure box from the chat's transcript view. Every
 *  activation clears it rather than each success path remembering to: it is appended
 *  furniture the transcript repaint does not own. */
function clearChatLoadError(): void {
  for (const box of (activeTranscriptView() ?? $.messages).querySelectorAll(".load-error")) {
    box.remove();
  }
}

/** The transcript's failure affordance: what went wrong, and one button that tries
 *  again. Shared by the two ways a chat can fail to open — its messages did not load,
 *  or its record is not in this device's store at all — because the reader's move is
 *  the same either way. Retry re-activates. */
function paintChatLoadError(message: string, id: string): void {
  const box = el("div", { className: "load-error" }, el("span", {}, message));
  const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
  btn.addEventListener("click", () => {
    activateChatView(id);
  });
  box.appendChild(btn);
  (activeTranscriptView() ?? $.messages).appendChild(box);
}

/** Re-read the chat list for a tab whose chat this device's store does not hold, and
 *  activate it again if the read produces it. One attempt per activation, and it cannot
 *  loop. The generation check is what makes it safe to fire and forget. */
async function healMissingChat(id: string, gen: number): Promise<void> {
  if (!(await loadList())) {
    return;
  }
  if (getActiveId() !== id || activationGen !== gen || get(id) === undefined) {
    return;
  }
  activateChatView(id);
}

/** Point every per-chat view at `id` and load its transcript. A chat tab's `onShow`,
 *  exported so the tab factory can name it. */
export function activateChatView(id: string): void {
  // The save MUST precede setActive: it reads the outgoing chat's id, which nothing
  // can recover afterwards.
  saveComposerState();
  const gen = ++activationGen;
  // Re-keyed on the switch itself, before the repaint below and before any branch or
  // early return can skip it: as a side effect of a successful message load, every
  // path that never reaches that callback inherited the previous chat's markers.
  // Pointing the rail is the whole update here — the rail spans the SESSION, and the
  // loaded branch fetches for itself below.
  pointTurnRail(id);
  setActive(id);
  restoreComposerState(id);
  ensureBound();
  clearChatLoadError();
  const session = get(id);
  if (session === undefined) {
    // A tab naming a chat this device's store does not hold: the store is provably
    // stale against a set the server minted, so the answer is one re-read. The
    // affordance is painted FIRST, so a re-read that fails leaves something to act on.
    paintChatLoadError("This conversation isn't loaded yet.", id);
    void healMissingChat(id, gen);
    return;
  }

  if (session.usage.context_size === 0 && session.model !== "") {
    // setModel recomputes the derived usage.context_size; mutating `session.usage`
    // directly writes to a reference subscribers never see.
    setModel(id, session.model);
  }

  if (isEmptyChat(session)) {
    // The picker shows itself — its visibility is an effect over this same predicate
    // (picker.ts bindVisibility). Only the draft is this branch's, and the record GET
    // exists only to adopt the server-held draft, so it is gated like the loaded branch.
    if (transcriptStale(session)) {
      seedEmptyChatDraft(id);
    }
  } else if (!transcriptStale(session)) {
    // The window is the server's answer and nothing has undermined it since, so
    // switching back costs ZERO fetches. The rail decides its own fetch off its record
    // — the message count moves under background SSE ingest.
    setupLoadMore(id);
    void loadTurnRail(id);
  } else {
    // A SKELETON MAY ONLY PAINT OVER AN EMPTY TRANSCRIPT: the repaint above runs
    // synchronously off setActive, so on a loaded chat it would stack a placeholder
    // under the whole conversation for the length of the refresh round trip.
    //
    // On a cold transcript the paint is deferred by 150ms, so a cached open never
    // flashes it. min-visible stays 0 — the skeleton shares the messages container.
    let skeletonPainted = false;
    const skeleton =
      session.messages.length > 0
        ? null
        : skeletonTiming(() => {
            skeletonPainted = true;
            const skel = chatSkeleton();
            // Into the ACTIVE VIEW: the view's own column geometry positions the
            // placeholder. The multiplexer fallback covers a fixture with no view.
            (activeTranscriptView() ?? $.messages).appendChild(skel);
            return () => {
              skel.remove();
            };
          });
    void loadMessages(id).then((ok) => {
      skeleton?.cancel();
      if (getActiveId() !== id || activationGen !== gen) {
        return;
      }
      if (!ok) {
        paintChatLoadError("Failed to load messages.", id);
        return;
      }
      // loadMessages' own bumpMessages paints synchronously, so the turns are already
      // in the DOM and no frame has reached the screen between the two — one
      // transition rather than a flash of both. Only when a skeleton was painted.
      if (skeletonPainted) {
        fadeInTranscript();
      }
      // The chat's record is in now, so its stored draft can be adopted. Deliberately
      // loses to a draft the user has started typing since the activation.
      seedComposerState(id);
      const fresh = get(id);
      if (fresh !== undefined) {
        setupLoadMore(id);
      }
      // The rail's index is session-wide and independent of the message window, so it
      // is its own fetch. FORCED: the load that just landed re-stamped the session
      // fresh, so the rail's gate can no longer see this activation's stale verdict.
      void loadTurnRail(id, { force: true });
    });
  }
}

/** Fetch a message-less chat's record so its stored draft can be adopted.
 *
 *  The draft rides the single-chat GET, and the branch above skips that GET for a chat
 *  with no messages — which is exactly the chat that can still hold one. The re-check
 *  after the await stays: the user can switch chats while it is in flight, and the
 *  seed writes into the shared composer. */
function seedEmptyChatDraft(id: string): void {
  void loadMessages(id).then((ok) => {
    if (ok && getActiveId() === id) {
      seedComposerState(id);
    }
  });
}

// --- Pagination ---

function setupLoadMore(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) {
    return;
  }
  setLoadMore(
    session.has_more
      ? (): void => {
          const oldest = session.messages[0];
          if (oldest === undefined) {
            return;
          }
          void loadMessages(chatID, oldest.id).then(() => {
            // scroll.ts watches for this removal as the "load complete" signal.
            // Scoped to this chat's view: parked views keep the id resident too.
            transcriptViewFor(chatID)?.querySelector(`[id="load-more-skeleton"]`)?.remove();
            if (getActiveId() !== chatID) {
              return;
            }
            const s = get(chatID);
            if (s === undefined) {
              return;
            }
            // Store mutations bump version; the chat-view effect re-renders.
            setupLoadMore(chatID);
          });
        }
      : null,
    session.has_more,
  );
}

// --- Sending prompts ---

/** Send from the composer. Nothing here touches the model picker: `submitPrompt` sets
 *  `thinking` synchronously and the picker's own effect keys on it, so the overlay
 *  closes for every sender. */
export function sendPrompt(text: string): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  void submitPrompt(chatID, text);
}

// --- Session lifecycle ---

const NEW_CHAT_NAME = "New conversation";

/** Seed the store row for a chat the SERVER has just created. The id, name and model
 *  all come off the header the create command returned, so the row is a projection of
 *  what the server persisted rather than a guess. `usage.context_size` is the one
 *  derived field: the client owns the model catalog, so the header cannot carry it. */
function seedChat(header: ChatHeader): void {
  upsertHeader(header);
  const model = header.model ?? "";
  if (model !== "") {
    setModel(header.id, model);
  }
}

/** Adopt the tab a creating command opened server-side: paint it from the response,
 *  hand the pending-op machine what it committed, and activate it. `create_chat` and
 *  `fork_chat` write the record and open the tab under one coordinator lock, so the
 *  reply already carries the committed subject. A reply with no subject has nothing to
 *  adopt, so the pending op is retired. */
function adoptCreatedTab(opID: string, created: CreatedChat): void {
  const subject = created.subject;
  if (subject === undefined) {
    opFailed(opID);
    return;
  }
  adoptSubject(subject, created.chat.name === "" ? NEW_CHAT_NAME : created.chat.name);
  adoptCommitted(opID, subject, created.version, true);
  activateTab(subject.id);
}

/** Create a new chat and open its tab. Resolves to the new chat's id, or "" when the
 *  server refused. If initialPrompt is non-empty, send it immediately.
 *
 *  ASYNC because the id is the SERVER's. Every caller must await or explicitly detach
 *  — `no-floating-promises` names the sites, but a bare `void` at one that reads
 *  `getActiveId()` on the next line would silently read the PREVIOUS chat. */
export async function createSession(initialPrompt?: string): Promise<string> {
  const model = getLastModel();
  // Minted HERE and passed as an argument, never inside the action's run(): the
  // framework re-runs run() per retry attempt, so the server would mint a second chat
  // for one gesture. Registered BEFORE the dispatch so the tabs frame correlates.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await createChat.dispatch({ opID, model });
  if (created === null) {
    // The action framework has already raised its toast; there is no chat.
    opFailed(opID);
    return "";
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // The composer belongs to THIS chat from here, not from the activation below —
  // otherwise anything typed in the round-trip window is filed under the chat the user
  // just left and flushed to the server as its draft.
  retargetComposer(id);
  // The reply carries the tab the create opened server-side, so the row is painted and
  // ACTIVATED from the response, with no second round trip.
  adoptCreatedTab(opID, created);

  if (initialPrompt !== undefined && initialPrompt !== "") {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  }
  return id;
}

/** Open a TANGENT off `parentChatID`: a real chat starting with the parent's whole
 *  conversation behind it, opened as a SUB-TAB under the one it came from.
 *
 *  `chat.fork` calls KAS's `session/fork` and binds the new chat to the session it
 *  returns, so no context is copied and nothing syncs the two. `TabSubject.Parent` is
 *  set at open and never reassigned, making a parent cycle unrepresentable. */
export async function openTangentChat(parentChatID: string): Promise<void> {
  if (parentChatID === "" || get(parentChatID) === undefined) {
    return;
  }
  const model = get(parentChatID)?.model ?? getLastModel();
  // The fork creates the chat and MINTS its id, so the sub-tab can only open once the
  // reply lands.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await forkChat.dispatch({ opID, parentChatID });
  if (created === null) {
    opFailed(opID);
    return;
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // The tangent is the active chat now, so the composer must be its own first.
  retargetComposer(id);
  // The reply's subject already carries the PARENT the coordinator nested the tangent
  // under, so adopting it paints the sub-tab and activates it with no second POST.
  adoptCreatedTab(opID, created);
  setCurrentModel(model);
}

/** Put the reader on this chat, OPENING its tab when it has none.
 *
 *  The open lives here rather than inlined in the router: this function is "put the
 *  reader on this chat" and has exactly one caller, and `chat.test.ts` can drive it.
 *  Rewriting a URL that names no record stays the router's. A REFUSAL IS NOT REPORTED
 *  HERE — `openTabCommand` declares its own error message, so the framework has
 *  already toasted; the outcome is returned so the caller can canonicalize the URL. */
export async function switchSession(id: string): Promise<OpenTabOutcome | "activated"> {
  // A chat id and its TAB id are different values — the tab's is opaque and
  // server-minted — so reaching a chat's tab goes through the lookup.
  const tabID = tabIdFor("chat", id);
  if (tabID !== "") {
    if (id === getActiveId() && getActiveTabId() === tabID) {
      return "activated";
    }
    activateTab(tabID);
    return "activated";
  }
  // No tab. The caller has already established the chat EXISTS, and only the router
  // can rewrite a URL naming no record, so this is the open.
  return openChatTab(id, get(id)?.name ?? "Chat");
}

/** Settle a deep-linked chat id the store holds NO row for, by ASKING the server.
 *
 *  The store's silence is not evidence: `loadList` makes it authoritative at the
 *  instant it lands and stale from then on, so a chat created on another device while
 *  this client's SSE lags is missing from it. Three answers — `opened` (the header is
 *  adopted, `switchSession` opens the tab), `gone` (the server says no such chat),
 *  `unresolved` (nobody answered; the caller says nothing terminal). The store is
 *  RE-READ after the await, so a row that appeared meanwhile outranks the verdict. */
export async function resolveUnknownChat(id: string): Promise<"opened" | "gone" | "unresolved"> {
  const verdict = await confirmChatExists(id);
  if (verdict === "exists" || get(id) !== undefined) {
    await switchSession(id);
    return "opened";
  }
  return verdict;
}

/** Attach workspace files to the active chat's next prompt, each as a removable pill
 *  below the textarea. Switches to the chat tab if needed.
 *
 *  PLURAL, and not a convenience: with the chat id coming from the server, N singular
 *  calls would each find no active chat and each ask for one, so a three-file drop
 *  onto an empty workspace would create three chats with one file each. */
export async function attachPathsToActiveChat(paths: readonly string[]): Promise<void> {
  if (paths.length === 0) {
    return;
  }
  let id = getActiveId();
  if (id === "") {
    id = await createSession();
    if (id === "") {
      return;
    }
  }
  const tabID = tabIdFor("chat", id);
  if (tabID !== "" && getActiveTabId() !== tabID) {
    activateTab(tabID);
  }
  for (const p of paths) {
    addAttachment(p);
  }
  $.promptInput.focus();
}

/** Open a session the previous-session picker listed.
 *
 *  Every row the picker offers is a TAB CONVERSATION — the server lists a session only
 *  when a vibekit chat claims it (`toResumable`) — so `chat_id` is always present. */
export async function openPreviousSession(
  row: ResumableSession,
): Promise<"opened" | "gone" | "failed"> {
  const chatID = row.chat_id ?? "";
  if (chatID === "") {
    return "failed";
  }
  const existing = get(chatID);
  if (existing === undefined) {
    // Required, not defensive: closing a tab calls removeChat, so a chat closed in
    // this browser session is gone from the store while its file survives. Activating
    // first renders an empty chat view and stops.
    await loadList();
  }
  // AWAITED: opening the tab is a round trip. `openChatTab` already activates, so the
  // explicit call below is the belt for a tab already open and active, where it refetches.
  const outcome = await openChatTab(chatID, get(chatID)?.name ?? row.title);
  if (outcome === "not-found") {
    // Retention is off and a close DELETED this conversation. Said with the activation
    // SKIPPED, or the reader lands on an empty transcript over a dead active pointer.
    // Distinct from a network failure by openTab's outcome.
    info("That conversation was ephemeral (retention is off) and is gone.");
    return "gone";
  }
  if (outcome !== "opened") {
    // The framework's error surface has already spoken; the row stays.
    return "failed";
  }
  activateChatView(chatID);
  return "opened";
}

/** Create a new chat pre-set to the "plan" workflow mode — the share-target
 *  `?agent=planner` shortcut. On KAS "planner" is the bundled Plan mode, not a v2
 *  agent: the mode is persisted on the empty chat and applied at session/new
 *  (StartOpts.Mode). AWAITS the create — `set_mode` addresses the chat it returned. */
export async function createPlannerSession(): Promise<void> {
  const id = await createSession();
  if (id === "") {
    return;
  }
  void setMode.dispatch({ chatID: id, modeID: "plan" });
}

/** A chat tab's hover tooltip: its mode, then what the agent says it is doing. Either
 *  half can be missing — a chat with no session has no mode, and the agent declares a
 *  description only while working — so the separator is emitted only when both are. */
function tabTooltipFor(s: Session): string {
  const mode = s.current_mode_id === "" ? "" : labelForMode(s.current_mode_id, s.available_modes);
  const doing = s.agent_status_text ?? "";
  if (mode === "" || doing === "") {
    return mode === "" ? doing : mode;
  }
  return `${mode} · ${doing}`;
}

/** One open chat tab's row effect. Tracks THIS chat's per-entity signal (plus the
 *  session set's structure, so a row landing after its tab still paints) and the
 *  decision dock, and writes only its own row. The dock read doubles as the
 *  subscription, which is the case the dot's "input" state exists for. */
function chatRowEffect(chatID: string): () => void {
  return effect(() => {
    const s = watchSession(chatID);
    const pendingAsk = hasPendingDecision(chatID);
    if (s === undefined) {
      return;
    }
    // ONE lookup, reused by all three writers below; a chat id is not the row's id.
    const tabID = tabIdFor("chat", chatID);
    if (tabID === "") {
      return;
    }
    // Reconcile tab name with server auto-rename / agent focus title.
    renameTab(tabID, s.name);
    // tabStatusFor owns the precedence; the pending-ask half comes from the dock.
    setTabStatus(tabID, tabStatusFor(s, pendingAsk));
    // The mode half is here because the dot took the slot the per-mode role glyph held,
    // and for a BACKGROUND chat that was the only place a role read out at all — the
    // mode pill and its picker are active-chat only. Pointer-only.
    setTabTooltip(tabID, tabTooltipFor(s));
  });
}

/** The previous install's teardown: a re-install (tests) tears the old wiring down
 *  first so row effects cannot stack and double-write. */
let disposeInstall: (() => void) | undefined;

/** Wire the tab strip's per-open-row effects and the context bar's active-session
 *  effect. Also mounts the chat view (idempotent). */
export function installStoreSubscribers(): void {
  mountChatView();
  disposeInstall?.();

  // The context bar tracks the ACTIVE session only: a background session's churn never
  // reaches it. Covers activation and every store write to the active chat.
  const disposeContext = effect(() => {
    const active = activeSession.value;
    if (active !== undefined) {
      refreshContextUI(active);
    }
  });

  // Row effects appear and disappear with open tabs, so a session with no open tab is
  // subscribed to by nothing and triggers nothing.
  const rowEffects = new Map<string, () => void>();
  const disposeSync = effect(() => {
    const open = openChatRefs();
    const openSet = new Set(open);
    for (const [chatID, dispose] of rowEffects) {
      if (!openSet.has(chatID)) {
        dispose();
        rowEffects.delete(chatID);
      }
    }
    for (const chatID of open) {
      if (!rowEffects.has(chatID)) {
        rowEffects.set(chatID, chatRowEffect(chatID));
      }
    }
  });

  disposeInstall = () => {
    disposeSync();
    for (const dispose of rowEffects.values()) {
      dispose();
    }
    rowEffects.clear();
    disposeContext();
  };
}
