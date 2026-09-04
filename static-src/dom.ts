// ---------------------------------------------------------------------------
// DOM element registry: query all elements once at startup.
// Fails fast if an element is missing instead of crashing later.
//
// `el<T>(id)` is the single lookup primitive used by `$` below and by any
// feature module whose DOM ids aren't worth registering on the global
// Elements class. Modal-local ids (tool-*, filepicker-*, etc.) stay in
// their own modules but use this helper instead of redefining it.
//
// A GETTER NOTHING READS IS DELETED, not left as documentation. 43 of 154 had no
// `$.<name>` reader (2026-08), and 20 of those looked up an id that exists in no
// HTML — `byId` throws on a missing element, so each was a call that could only
// ever raise. Three whole regions went with them (the single-repo git panel, the
// PR panel, the CI pill): those features build their DOM in TS now, so their
// registry entries described markup that no longer exists. The check is one
// grep, `$.<name>` against this file's getter list, and nothing dynamic defeats
// it — `$` is never aliased on import, never destructured and never indexed.
// ---------------------------------------------------------------------------

/** Look up a DOM element by id. Throws if missing. Use this instead of
 *  bare `document.getElementById(...) as HTMLFoo` — it fails fast with a
 *  readable error rather than NPE'ing on the next property access. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- caller uses T for inference: el<HTMLInputElement>("id")
export function byId<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (e === null) {
    throw new Error(`Missing element: #${id}`);
  }
  return e as T;
}

/** Like el() but returns null when the element doesn't exist.
 *  Use for elements that are conditionally present in the DOM.
 *  T parameter only appears in the return type intentionally — the
 *  call site declares which element subclass it expects, mirroring the
 *  built-in `document.getElementById<T>` ergonomics. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- T is the call-site contract for the returned element subclass
export function maybeEl<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

/** Force a synchronous style and layout flush for `el`.
 *
 *  The browser coalesces style mutations, so removing a class and re-adding it
 *  in one task is not a change and restarts no animation. Reading a layout
 *  property between the two makes the removal land in a completed style
 *  resolution, which is what separates the two writes.
 *
 *  Returns the value it read, and the callers discard it. That is deliberate:
 *  the read IS the side effect, and a returned value keeps it a call rather
 *  than an expression statement, which reads as dead code to anyone (and to
 *  `@typescript-eslint/no-unused-expressions`).
 *
 *  Takes `Element` rather than `HTMLElement` because `getBoundingClientRect`
 *  is on `Element`; `offsetWidth` is not, and every call site that used it
 *  needed a cast to say so. */
export function forceReflow(el: Element): number {
  return el.getBoundingClientRect().height;
}

/** Mark any element as BUSY — its content or its work is in flight — or clear it.
 *
 *  THE ONLY PLACE THE VALUE IS SPELLED, and that is the point. `aria-busy` is a
 *  boolean-typed ARIA attribute, so its value must be the literal `"true"`;
 *  `toggleAttribute("aria-busy", true)` writes the empty string, which is invalid
 *  and therefore treated as the default of false. Measured in Chromium against
 *  the accessibility tree: the empty form yields NO busy property at all, exactly
 *  like an element with no attribute, while `"true"` yields `busy: 1`. It also
 *  fails `[aria-busy="true"]`, so the busy face never paints either. Two callers
 *  had it — the model grid and the model list, each announcing nothing while
 *  their catalogue loaded. */
export function setBusy(el: Element, busy: boolean): void {
  if (busy) {
    el.setAttribute("aria-busy", "true");
  } else {
    el.removeAttribute("aria-busy");
  }
}

/** Mark a CONTROL busy: `disabled` and `aria-busy` together.
 *
 *  That pair is what the two readers need. Assistive tech announces the busy
 *  state off the attribute, and `40-a11y.css`'s busy face paints off the same
 *  one, so a control cannot look busy without saying so or the reverse. Callers
 *  that disable around an `await` had been setting `disabled` alone, which took
 *  the UNAVAILABLE face — dimmed, `not-allowed` — while their own label read
 *  "Suggesting…" or "Delivering…".
 *
 *  Not for a control that is unavailable rather than working: that is `disabled`
 *  on its own, and it should keep the refusal face. `bindLoadingState` from
 *  `@cplieger/actions` already sets both, so an action-bound control needs
 *  nothing here. */
export function setControlBusy(el: HTMLButtonElement | HTMLInputElement, busy: boolean): void {
  el.disabled = busy;
  setBusy(el, busy);
}

// Lazy singleton: elements are queried on first access via getter.
// This allows the module to be imported before DOMContentLoaded
// as long as no property is accessed until the DOM is ready.
class Elements {
  // Sidebar
  get sidebar(): HTMLElement {
    return byId("sidebar");
  }
  get tabList(): HTMLDivElement {
    return byId("tab-list");
  }
  get newChatBtn(): HTMLButtonElement {
    return byId("new-chat");
  }
  get menuToggle(): HTMLButtonElement {
    return byId("menu-toggle");
  }
  get sidebarClose(): HTMLButtonElement {
    return byId("sidebar-close");
  }
  get settingsBtn(): HTMLButtonElement {
    return byId("settings-btn");
  }
  get statusDot(): HTMLButtonElement {
    return byId("status-dot");
  }
  get userEmail(): HTMLElement {
    return byId("user-email");
  }
  get logoutBtn(): HTMLButtonElement {
    return byId("logout-btn");
  }

  // Chat
  get messages(): HTMLDivElement {
    return byId("messages");
  }
  get messagesWrap(): HTMLDivElement {
    return byId("messages-wrap");
  }
  // The positioned wrapper AROUND the scroller. The timeline rail mounts here
  // rather than inside #messages-wrap so it stays put instead of scrolling away
  // with the transcript.
  get messagesWrapOuter(): HTMLDivElement {
    return byId("messages-wrap-outer");
  }
  get bannerStack(): HTMLDivElement {
    return byId("banner-stack");
  }
  get scrollBottom(): HTMLButtonElement {
    return byId("scroll-bottom");
  }
  get modelPicker(): HTMLDivElement {
    return byId("model-picker");
  }
  get promptForm(): HTMLFormElement {
    return byId("prompt-form");
  }
  get promptInput(): HTMLTextAreaElement {
    return byId("prompt-input");
  }
  get attachmentRow(): HTMLUListElement {
    return byId("attachment-row");
  }
  get steerStack(): HTMLUListElement {
    return byId("steer-stack");
  }
  get sendBtn(): HTMLButtonElement {
    return byId("send-btn");
  }
  get switchModelBtn(): HTMLButtonElement {
    return byId("switch-model-btn");
  }
  get modelSwitchList(): HTMLDivElement {
    return byId("model-switch-list");
  }
  get rolePill(): HTMLButtonElement {
    return byId("role-pill");
  }
  get roleList(): HTMLDivElement {
    return byId("role-list");
  }
  /** The interaction dock's host. One region replaced the three decision
   *  <dialog>s (tool-approval, elicitation-dialog, user-input-dialog). */
  get decisionDock(): HTMLDivElement {
    return byId("decision-dock");
  }
  get contextIndicator(): HTMLButtonElement {
    return byId("context-indicator");
  }
  get contextRingFill(): HTMLElement {
    return byId("context-ring-fill");
  }
  get contextLabel(): HTMLElement {
    return byId("context-label");
  }

  // Context popup
  get ctxModelPill(): HTMLElement {
    return byId("ctx-model-pill");
  }
  /** The reasoning tier beside the model name on the model pill. Its own element
   *  so the model name keeps the ellipsis and the tier is never the half that
   *  gets clipped; `.hidden` when the chat runs at the model's default. */
  get ctxEffortPill(): HTMLElement {
    return byId("ctx-effort-pill");
  }
  get ctxTokens(): HTMLElement {
    return byId("ctx-tokens");
  }
  get ctxCredits(): HTMLElement {
    return byId("ctx-credits");
  }
  get ctxTurns(): HTMLElement {
    return byId("ctx-turns");
  }
  get ctxLastTurn(): HTMLElement {
    return byId("ctx-last-turn");
  }
  get ctxMsgs(): HTMLElement {
    return byId("ctx-msgs");
  }
  get ctxTools(): HTMLElement {
    return byId("ctx-tools");
  }
  get ctxMetering(): HTMLElement {
    return byId("ctx-metering");
  }

  // Status popup
  //
  // The card is the dot's SIBLING (see 15-input.css .pill-slot), so it
  // inherits nothing from the dot: status.ts writes --status-color onto the
  // card itself.
  get statusCard(): HTMLElement {
    return byId("status-card");
  }
  get stWs(): HTMLElement {
    return byId("st-ws");
  }
  get stKiro(): HTMLElement {
    return byId("st-kiro");
  }
  get stAuth(): HTMLElement {
    return byId("st-auth");
  }
  get stAccount(): HTMLElement {
    return byId("st-account");
  }
  get acctPlan(): HTMLElement {
    return byId("acct-plan");
  }
  get acctMeter(): HTMLElement {
    return byId("acct-meter");
  }

  // Settings
  get steeringInput(): HTMLTextAreaElement {
    return byId("steering-input");
  }
  get toolUpdateBtn(): HTMLButtonElement {
    return byId("tool-update-btn");
  }
  get toolUpdateOutput(): HTMLDivElement {
    return byId("tool-update-output");
  }
  get toolAddBtn(): HTMLButtonElement {
    return byId("tool-add-btn");
  }
  get toolsList(): HTMLDivElement {
    return byId("tools-list");
  }

  // Shell
  get shellPanel(): HTMLDivElement {
    return byId("shell-panel");
  }
  get shellBtn(): HTMLButtonElement {
    return byId("shell-btn");
  }
  get shellToggleBtn(): HTMLButtonElement {
    return byId("shell-toggle-btn");
  }
  get shellRestartBtn(): HTMLButtonElement {
    return byId("shell-restart-btn");
  }
  get shellKeysBtn(): HTMLButtonElement {
    return byId("shell-keys-btn");
  }
  get shellFullscreenBtn(): HTMLButtonElement {
    return byId("shell-fullscreen-btn");
  }
  get shellTerminal(): HTMLDivElement {
    return byId("shell-terminal");
  }
  get shellResize(): HTMLDivElement {
    return byId("shell-resize");
  }

  // Git
  get gitBtn(): HTMLButtonElement {
    return byId("git-btn");
  }
  get gitBadge(): HTMLElement {
    return byId("git-badge");
  }

  // File browser
  get filesBtn(): HTMLButtonElement {
    return byId("files-btn");
  }
  get fbList(): HTMLDivElement {
    return byId("fb-list");
  }
  get fbBack(): HTMLButtonElement {
    return byId("fb-back");
  }
  get fbForward(): HTMLButtonElement {
    return byId("fb-forward");
  }
  get fbPath(): HTMLInputElement {
    return byId("fb-path");
  }
  get fbUpload(): HTMLButtonElement {
    return byId("fb-upload");
  }
  get fbDownload(): HTMLButtonElement {
    return byId("fb-download");
  }
  get fbNewFile(): HTMLButtonElement {
    return byId("fb-new-file");
  }
  get fbNewFolder(): HTMLButtonElement {
    return byId("fb-new-folder");
  }
  get fbAddToChat(): HTMLButtonElement {
    return byId("fb-add-to-chat");
  }
  get fbChatFilter(): HTMLButtonElement {
    return byId("fb-chat-filter");
  }
  // Chat options (the composer's set-once switches menu)
  get chatOptionsBtn(): HTMLButtonElement {
    return byId("chat-options-btn");
  }
  get chatOptionsCard(): HTMLElement {
    return byId("chat-options-card");
  }
  get fbRename(): HTMLButtonElement {
    return byId("fb-rename");
  }
  get fbDelete(): HTMLButtonElement {
    return byId("fb-delete");
  }
  get fbDropOverlay(): HTMLDivElement {
    return byId("fb-drop-overlay");
  }

  // History
  get historyBtn(): HTMLButtonElement {
    return byId("history-btn");
  }

  // Transcript search. The Ctrl+F overlay's toolbar trigger — the hotkey used
  // to be the only door, which left the feature undiscoverable and unreachable
  // without a keyboard.
  get findBtn(): HTMLButtonElement {
    return byId("find-btn");
  }

  // Kiro configuration browser (the book icon's page)
  get docsBtn(): HTMLButtonElement {
    return byId("docs-btn");
  }
  get docsView(): HTMLDivElement {
    return byId("docs-view");
  }
  get docsTabBar(): HTMLElement {
    return byId("docs-tab-bar");
  }

  // Editor
  get editorContent(): HTMLTextAreaElement {
    return byId("editor-content");
  }
  get editorHighlight(): HTMLPreElement {
    return byId("editor-highlight");
  }
  get editorCode(): HTMLElement {
    return byId("editor-code");
  }
  get editorGutter(): HTMLPreElement {
    return byId("editor-gutter");
  }
  get editorFilename(): HTMLElement {
    return byId("editor-filename");
  }
  get editorError(): HTMLElement {
    return byId("editor-error");
  }
  get editorEditBtn(): HTMLButtonElement {
    return byId("editor-edit-btn");
  }
  get editorSaveBtn(): HTMLButtonElement {
    return byId("editor-save-btn");
  }
  get editorCancelBtn(): HTMLButtonElement {
    return byId("editor-cancel-btn");
  }
  get editorDiffBtn(): HTMLButtonElement {
    return byId("editor-diff-btn");
  }
  get editorMarkdown(): HTMLDivElement {
    return byId("editor-markdown");
  }
  get editorImage(): HTMLDivElement {
    return byId("editor-image");
  }
  get editorDiffPane(): HTMLDivElement {
    return byId("editor-diff-pane");
  }
  get editorConflictOverlay(): HTMLDivElement {
    return byId("editor-conflict-overlay");
  }
  // Modals
  get loginModal(): HTMLDivElement {
    return byId("login-modal");
  }
  get toolModal(): HTMLDivElement {
    return byId("tool-modal");
  }

  // Settings panel (extra getters added by api-client migration)
  get notifyToggle(): HTMLInputElement {
    return byId("notify-toggle");
  }
  get notifyHint(): HTMLParagraphElement {
    return byId("notify-hint");
  }
  get notifySubOptions(): HTMLDivElement {
    return byId("notify-sub-options");
  }

  // Settings tab bar (mobile dropdown + desktop segmented control)
  get settingsTabBar(): HTMLDivElement {
    return byId("settings-tab-bar");
  }

  // MCP modal (shared by add + edit)
  get mcpModal(): HTMLDivElement {
    return byId("mcp-modal");
  }

  // Upload progress bar (shared UI at the bottom of viewport)
  get uploadProgress(): HTMLDivElement {
    return byId("upload-progress");
  }
  get uploadProgressFill(): HTMLDivElement {
    return byId("upload-progress-fill");
  }
  get uploadProgressLabel(): HTMLElement {
    return byId("upload-progress-label");
  }
  get uploadProgressCancel(): HTMLButtonElement {
    return byId("upload-progress-cancel");
  }

  // Theme toggle
  get themeBtn(): HTMLButtonElement {
    return byId("theme-btn");
  }

  // Tabs / shell
  // Startup
  get appRoot(): HTMLElement {
    return byId("app");
  }
  get chatArea(): HTMLElement {
    return byId("chat-area");
  }
}

export const $ = new Elements();
