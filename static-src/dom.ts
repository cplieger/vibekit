// ---------------------------------------------------------------------------
// DOM element registry: query all elements once at startup.
// Fails fast if an element is missing instead of crashing later.
//
// `el<T>(id)` is the single lookup primitive used by `$` below and by any
// feature module whose DOM ids aren't worth registering on the global
// Elements class. Modal-local ids (tool-*, filepicker-*, etc.) stay in
// their own modules but use this helper instead of redefining it.
// ---------------------------------------------------------------------------

import { viewTransition } from "@cplieger/ui-primitives/view-transition";

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
  get queuedRow(): HTMLUListElement {
    return byId("queued-row");
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
  get toolApproval(): HTMLDialogElement {
    return byId("tool-approval");
  }
  get elicitationDialog(): HTMLDialogElement {
    return byId("elicitation-dialog");
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
  get shellClearBtn(): HTMLButtonElement {
    return byId("shell-clear-btn");
  }
  get shellFullscreenBtn(): HTMLButtonElement {
    return byId("shell-fullscreen-btn");
  }
  get shellTerminal(): HTMLDivElement {
    return byId("shell-terminal");
  }
  get shellStatus(): HTMLElement {
    return byId("shell-status");
  }
  get shellTitle(): HTMLElement {
    return byId("shell-title-text");
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
  get gitBranchBtn(): HTMLButtonElement {
    return byId("git-branch-btn");
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

  // Specs board
  get specsBtn(): HTMLButtonElement {
    return byId("specs-btn");
  }
  get specsList(): HTMLDivElement {
    return byId("specs-list");
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
  get editorDiffPane(): HTMLDivElement {
    return byId("editor-diff-pane");
  }
  get editorConflictOverlay(): HTMLDivElement {
    return byId("editor-conflict-overlay");
  }
  get editorSendPlanBtn(): HTMLButtonElement {
    return byId("editor-send-plan-btn");
  }
  get editorPendingAcceptBtn(): HTMLButtonElement {
    return byId("editor-pending-accept-btn");
  }
  get editorPendingRejectBtn(): HTMLButtonElement {
    return byId("editor-pending-reject-btn");
  }
  get editorPendingApplyPartialBtn(): HTMLButtonElement {
    return byId("editor-pending-apply-partial-btn");
  }
  get editorPendingDiscussBtn(): HTMLButtonElement {
    return byId("editor-pending-discuss-btn");
  }
  get supervisedPill(): HTMLElement {
    return byId("supervised-pill");
  }

  // Modals
  get loginModal(): HTMLDivElement {
    return byId("login-modal");
  }
  get toolModal(): HTMLDivElement {
    return byId("tool-modal");
  }
  get gitOutputModal(): HTMLDivElement {
    return byId("git-output-modal");
  }
  get gitBranchModal(): HTMLDivElement {
    return byId("git-branch-modal");
  }

  // Git panel (added 2026 audit)
  get gitOutputBar(): HTMLDivElement {
    return byId("git-output-bar");
  }
  get gitRepoSection(): HTMLDivElement {
    return byId("git-repo-section");
  }
  get gitStagedSection(): HTMLDivElement {
    return byId("git-staged-section");
  }
  get gitStagedList(): HTMLDivElement {
    return byId("git-staged-list");
  }
  get gitChangedList(): HTMLDivElement {
    return byId("git-changed-list");
  }
  get gitLogList(): HTMLDivElement {
    return byId("git-log-list");
  }
  get gitRepoBar(): HTMLDivElement {
    return byId("git-repo-bar");
  }
  get gitCommitMsg(): HTMLTextAreaElement {
    return byId("git-commit-msg");
  }
  get gitNewBranch(): HTMLInputElement {
    return byId("git-new-branch");
  }
  get gitBranchList(): HTMLDivElement {
    return byId("git-branch-list");
  }
  get gitRefreshBtn(): HTMLButtonElement {
    return byId("git-refresh-btn");
  }
  get gitStageAllBtn(): HTMLButtonElement {
    return byId("git-stage-all-btn");
  }
  get gitUnstageAllBtn(): HTMLButtonElement {
    return byId("git-unstage-all-btn");
  }
  get gitDiscardAllBtn(): HTMLButtonElement {
    return byId("git-discard-all-btn");
  }
  get gitCommitBtn(): HTMLButtonElement {
    return byId("git-commit-btn");
  }
  get gitPushBtn(): HTMLButtonElement {
    return byId("git-push-btn");
  }
  get gitCreateBranchBtn(): HTMLButtonElement {
    return byId("git-create-branch-btn");
  }
  get gitStashBtn(): HTMLButtonElement {
    return byId("git-stash-btn");
  }
  get gitStashPopBtn(): HTMLButtonElement {
    return byId("git-stash-pop-btn");
  }

  // Kiro config viewer (list rendered into the Instructions tab)
  get kiroConfigList(): HTMLDivElement {
    return byId("kiro-config-list");
  }

  // PR panel
  get prSection(): HTMLElement {
    return byId("git-pr-section");
  }
  get prList(): HTMLDivElement {
    return byId("git-pr-list");
  }
  get prEmpty(): HTMLElement {
    return byId("git-pr-empty");
  }
  get prPlaceholder(): HTMLElement {
    return byId("git-pr-placeholder");
  }
  get prCreateDialog(): HTMLDialogElement {
    return byId("pr-create-dialog");
  }
  get prDialogStatus(): HTMLElement {
    return byId("pr-dialog-status");
  }
  get prBase(): HTMLInputElement {
    return byId("pr-base");
  }
  get prHead(): HTMLInputElement {
    return byId("pr-head");
  }
  get prTitle(): HTMLInputElement {
    return byId("pr-title");
  }
  get prBody(): HTMLTextAreaElement {
    return byId("pr-body");
  }
  get prDraft(): HTMLInputElement {
    return byId("pr-draft");
  }
  get prSubmitBtn(): HTMLButtonElement {
    return byId("pr-submit-btn");
  }
  get prGenerateBtn(): HTMLButtonElement {
    return byId("pr-generate-btn");
  }
  get prNewBtn(): HTMLButtonElement {
    return byId("git-pr-new-btn");
  }

  // CI pill
  get ciPill(): HTMLButtonElement {
    return byId("git-ci-pill");
  }
  get ciPanel(): HTMLDivElement {
    return byId("git-ci-panel");
  }

  // Settings panel (extra getters added by api-client migration)
  get settingsSaveStatus(): HTMLSpanElement {
    return byId("settings-save-status");
  }
  get notifyToggle(): HTMLInputElement {
    return byId("notify-toggle");
  }
  get notifyHint(): HTMLParagraphElement {
    return byId("notify-hint");
  }
  get notifySubOptions(): HTMLDivElement {
    return byId("notify-sub-options");
  }
  get notifyFinishedToggle(): HTMLInputElement {
    return byId("notify-finished-toggle");
  }
  get notifyPermissionToggle(): HTMLInputElement {
    return byId("notify-permission-toggle");
  }

  // Settings tab bar (mobile dropdown + desktop segmented control)
  get settingsTabBar(): HTMLDivElement {
    return byId("settings-tab-bar");
  }
  get settingsTabSelect(): HTMLSelectElement {
    return byId("settings-tab-select");
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
  get toolbarTitle(): HTMLElement {
    return byId("toolbar-title");
  }

  // Startup
  get appRoot(): HTMLElement {
    return byId("app");
  }
  get chatArea(): HTMLElement {
    return byId("chat-area");
  }
}

export const $ = new Elements();

// --- View Transition helper ---

/** Wrap a DOM swap in a queued, feature-detected view transition. Delegates to
 *  @cplieger/ui-primitives' `viewTransition`, which owns the feature detection
 *  and a serialization queue so overlapping swaps don't clash (an improvement
 *  over the old fire-and-forget local copy). Kept as a void-returning wrapper
 *  on the historical name so call sites (settings-tabs, files) and their test
 *  mocks are unchanged.
 *
 *  Hidden-document fast path (B5/E3, local half): startViewTransition's update
 *  callback needs a rendering opportunity, which a hidden/suspended tab may
 *  never get — the queued swap (and everything chained behind it) would wedge.
 *  Swap directly instead; there is nothing to animate while hidden anyway.
 *  The upstream ui-primitives watchdog covers the visible-but-suspended case. */
export function maybeViewTransition(fn: () => void): void {
  if (document.hidden) {
    fn();
    return;
  }
  void viewTransition(fn);
}
