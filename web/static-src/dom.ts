// ---------------------------------------------------------------------------
// DOM element registry: query all elements once at startup.
// Fails fast if an element is missing instead of crashing later.
//
// `el<T>(id)` is the single lookup primitive used by `$` below and by any
// feature module whose DOM ids aren't worth registering on the global
// Elements class. Modal-local ids (tool-*, filepicker-*, etc.) stay in
// their own modules but use this helper instead of redefining it.
// ---------------------------------------------------------------------------

/** Look up a DOM element by id. Throws if missing. Use this instead of
 *  bare `document.getElementById(...) as HTMLFoo` — it fails fast with a
 *  readable error rather than NPE'ing on the next property access. */
export function el<T extends HTMLElement>(id: string): T {
   
  const e = document.getElementById(id);
  if (e === null) {
    throw new Error(`Missing element: #${id}`);
  }
  return e as T;
}

// Lazy singleton: elements are queried on first access via getter.
// This allows the module to be imported before DOMContentLoaded
// as long as no property is accessed until the DOM is ready.
class Elements {
  // Sidebar
  get sidebar(): HTMLElement {
    return el("sidebar");
  }
  get tabList(): HTMLDivElement {
    return el("tab-list");
  }
  get newChatBtn(): HTMLButtonElement {
    return el("new-chat");
  }
  get newPlanBtn(): HTMLButtonElement {
    return el("new-plan");
  }
  get menuToggle(): HTMLButtonElement {
    return el("menu-toggle");
  }
  get sidebarClose(): HTMLButtonElement {
    return el("sidebar-close");
  }
  get settingsBtn(): HTMLButtonElement {
    return el("settings-btn");
  }
  get statusDot(): HTMLButtonElement {
    return el("status-dot");
  }
  get userEmail(): HTMLElement {
    return el("user-email");
  }
  get logoutBtn(): HTMLButtonElement {
    return el("logout-btn");
  }

  // Chat
  get messages(): HTMLDivElement {
    return el("messages");
  }
  get messagesWrap(): HTMLDivElement {
    return el("messages-wrap");
  }
  get bannerStack(): HTMLDivElement {
    return el("banner-stack");
  }
  get scrollBottom(): HTMLButtonElement {
    return el("scroll-bottom");
  }
  get modelPicker(): HTMLDivElement {
    return el("model-picker");
  }
  get promptForm(): HTMLFormElement {
    return el("prompt-form");
  }
  get promptInput(): HTMLTextAreaElement {
    return el("prompt-input");
  }
  get attachmentRow(): HTMLUListElement {
    return el("attachment-row");
  }
  get sendBtn(): HTMLButtonElement {
    return el("send-btn");
  }
  get followAlongBtn(): HTMLButtonElement {
    return el("follow-along-btn");
  }
  get autoApproveCrewBtn(): HTMLButtonElement {
    return el("auto-approve-crew-btn");
  }
  get switchModelBtn(): HTMLButtonElement {
    return el("switch-model-btn");
  }
  get modelSwitchList(): HTMLDivElement {
    return el("model-switch-list");
  }
  get toolApproval(): HTMLDialogElement {
    return el("tool-approval");
  }
  get contextIndicator(): HTMLButtonElement {
    return el("context-indicator");
  }
  get contextRingFill(): HTMLElement {
    return el("context-ring-fill");
  }
  get contextLabel(): HTMLElement {
    return el("context-label");
  }

  // Context popup
  get ctxModelPill(): HTMLElement {
    return el("ctx-model-pill");
  }
  get ctxTokens(): HTMLElement {
    return el("ctx-tokens");
  }
  get ctxCredits(): HTMLElement {
    return el("ctx-credits");
  }
  get ctxTurns(): HTMLElement {
    return el("ctx-turns");
  }
  get ctxLastTurn(): HTMLElement {
    return el("ctx-last-turn");
  }
  get ctxMsgs(): HTMLElement {
    return el("ctx-msgs");
  }
  get ctxTools(): HTMLElement {
    return el("ctx-tools");
  }
  get ctxMetering(): HTMLElement {
    return el("ctx-metering");
  }

  // Status popup
  get stWs(): HTMLElement {
    return el("st-ws");
  }
  get stKiro(): HTMLElement {
    return el("st-kiro");
  }
  get stAuth(): HTMLElement {
    return el("st-auth");
  }

  // Settings
  get steeringInput(): HTMLTextAreaElement {
    return el("steering-input");
  }
  get autoUpdateToggle(): HTMLInputElement {
    return el("auto-update-toggle");
  }
  get toolUpdateBtn(): HTMLButtonElement {
    return el("tool-update-btn");
  }
  get toolUpdateOutput(): HTMLDivElement {
    return el("tool-update-output");
  }
  get toolAddBtn(): HTMLButtonElement {
    return el("tool-add-btn");
  }
  get toolsList(): HTMLDivElement {
    return el("tools-list");
  }

  // Shell
  get shellPanel(): HTMLDivElement {
    return el("shell-panel");
  }
  get shellBtn(): HTMLButtonElement {
    return el("shell-btn");
  }
  get shellToggleBtn(): HTMLButtonElement {
    return el("shell-toggle-btn");
  }
  get shellClearBtn(): HTMLButtonElement {
    return el("shell-clear-btn");
  }
  get shellKillBtn(): HTMLButtonElement {
    return el("shell-kill-btn");
  }
  get shellFullscreenBtn(): HTMLButtonElement {
    return el("shell-fullscreen-btn");
  }
  get shellTerminal(): HTMLDivElement {
    return el("shell-terminal");
  }
  get shellStatus(): HTMLElement {
    return el("shell-status");
  }
  get shellTitle(): HTMLElement {
    return el("shell-title-text");
  }
  get shellResize(): HTMLDivElement {
    return el("shell-resize");
  }

  // Git
  get gitBtn(): HTMLButtonElement {
    return el("git-btn");
  }
  get gitBadge(): HTMLElement {
    return el("git-badge");
  }
  get gitBranchBtn(): HTMLButtonElement {
    return el("git-branch-btn");
  }

  // File browser
  get filesBtn(): HTMLButtonElement {
    return el("files-btn");
  }
  get fbList(): HTMLDivElement {
    return el("fb-list");
  }
  get fbBack(): HTMLButtonElement {
    return el("fb-back");
  }
  get fbForward(): HTMLButtonElement {
    return el("fb-forward");
  }
  get fbPath(): HTMLInputElement {
    return el("fb-path");
  }
  get fbUpload(): HTMLButtonElement {
    return el("fb-upload");
  }
  get fbDownload(): HTMLButtonElement {
    return el("fb-download");
  }
  get fbNewFile(): HTMLButtonElement {
    return el("fb-new-file");
  }
  get fbNewFolder(): HTMLButtonElement {
    return el("fb-new-folder");
  }
  get fbAddToChat(): HTMLButtonElement {
    return el("fb-add-to-chat");
  }
  get fbRename(): HTMLButtonElement {
    return el("fb-rename");
  }
  get fbDelete(): HTMLButtonElement {
    return el("fb-delete");
  }
  get fbDropOverlay(): HTMLDivElement {
    return el("fb-drop-overlay");
  }

  // History
  get historyBtn(): HTMLButtonElement {
    return el("history-btn");
  }

  // Editor
  get editorContent(): HTMLTextAreaElement {
    return el("editor-content");
  }
  get editorHighlight(): HTMLPreElement {
    return el("editor-highlight");
  }
  get editorCode(): HTMLElement {
    return el("editor-code");
  }
  get editorGutter(): HTMLPreElement {
    return el("editor-gutter");
  }
  get editorFilename(): HTMLElement {
    return el("editor-filename");
  }
  get editorError(): HTMLElement {
    return el("editor-error");
  }
  get editorEditBtn(): HTMLButtonElement {
    return el("editor-edit-btn");
  }
  get editorSaveBtn(): HTMLButtonElement {
    return el("editor-save-btn");
  }
  get editorCancelBtn(): HTMLButtonElement {
    return el("editor-cancel-btn");
  }
  get editorDiffBtn(): HTMLButtonElement {
    return el("editor-diff-btn");
  }
  get editorDiffPane(): HTMLDivElement {
    return el("editor-diff-pane");
  }
  get editorConflictOverlay(): HTMLDivElement {
    return el("editor-conflict-overlay");
  }
  get editorSendPlanBtn(): HTMLButtonElement {
    return el("editor-send-plan-btn");
  }
  get editorPendingAcceptBtn(): HTMLButtonElement {
    return el("editor-pending-accept-btn");
  }
  get editorPendingRejectBtn(): HTMLButtonElement {
    return el("editor-pending-reject-btn");
  }
  get editorPendingApplyPartialBtn(): HTMLButtonElement {
    return el("editor-pending-apply-partial-btn");
  }
  get editorPendingDiscussBtn(): HTMLButtonElement {
    return el("editor-pending-discuss-btn");
  }
  get supervisedPill(): HTMLElement {
    return el("supervised-pill");
  }

  // Modals
  get loginModal(): HTMLDivElement {
    return el("login-modal");
  }
  get toolModal(): HTMLDivElement {
    return el("tool-modal");
  }
  get gitOutputModal(): HTMLDivElement {
    return el("git-output-modal");
  }
  get gitBranchModal(): HTMLDivElement {
    return el("git-branch-modal");
  }
  get subagentModal(): HTMLDivElement {
    return el("subagent-modal");
  }

  // Git panel (added 2026 audit)
  get gitOutputBar(): HTMLDivElement {
    return el("git-output-bar");
  }
  get gitRepoSection(): HTMLDivElement {
    return el("git-repo-section");
  }
  get gitStagedSection(): HTMLDivElement {
    return el("git-staged-section");
  }
  get gitStagedList(): HTMLDivElement {
    return el("git-staged-list");
  }
  get gitChangedList(): HTMLDivElement {
    return el("git-changed-list");
  }
  get gitLogList(): HTMLDivElement {
    return el("git-log-list");
  }
  get gitRepoBar(): HTMLDivElement {
    return el("git-repo-bar");
  }
  get gitCommitMsg(): HTMLTextAreaElement {
    return el("git-commit-msg");
  }
  get gitNewBranch(): HTMLInputElement {
    return el("git-new-branch");
  }
  get gitBranchList(): HTMLDivElement {
    return el("git-branch-list");
  }
  get gitRefreshBtn(): HTMLButtonElement {
    return el("git-refresh-btn");
  }
  get gitStageAllBtn(): HTMLButtonElement {
    return el("git-stage-all-btn");
  }
  get gitUnstageAllBtn(): HTMLButtonElement {
    return el("git-unstage-all-btn");
  }
  get gitDiscardAllBtn(): HTMLButtonElement {
    return el("git-discard-all-btn");
  }
  get gitCommitBtn(): HTMLButtonElement {
    return el("git-commit-btn");
  }
  get gitPushBtn(): HTMLButtonElement {
    return el("git-push-btn");
  }
  get gitPullBtn(): HTMLButtonElement {
    return el("git-pull-btn");
  }
  get gitOverflowBtn(): HTMLButtonElement {
    return el("git-overflow-btn");
  }
  get gitAiMsgBtn(): HTMLButtonElement {
    return el("git-ai-msg-btn");
  }
  get gitCreateBranchBtn(): HTMLButtonElement {
    return el("git-create-branch-btn");
  }
  get gitStashBtn(): HTMLButtonElement {
    return el("git-stash-btn");
  }
  get gitStashPopBtn(): HTMLButtonElement {
    return el("git-stash-pop-btn");
  }

  // Kiro config viewer (list rendered into the Instructions tab)
  get kiroConfigList(): HTMLDivElement {
    return el("kiro-config-list");
  }

  // PR panel
  get prSection(): HTMLElement {
    return el("git-pr-section");
  }
  get prList(): HTMLDivElement {
    return el("git-pr-list");
  }
  get prEmpty(): HTMLElement {
    return el("git-pr-empty");
  }
  get prPlaceholder(): HTMLElement {
    return el("git-pr-placeholder");
  }
  get prCreateDialog(): HTMLDialogElement {
    return el("pr-create-dialog");
  }
  get prDialogStatus(): HTMLElement {
    return el("pr-dialog-status");
  }
  get prBase(): HTMLInputElement {
    return el("pr-base");
  }
  get prHead(): HTMLInputElement {
    return el("pr-head");
  }
  get prTitle(): HTMLInputElement {
    return el("pr-title");
  }
  get prBody(): HTMLTextAreaElement {
    return el("pr-body");
  }
  get prDraft(): HTMLInputElement {
    return el("pr-draft");
  }
  get prSubmitBtn(): HTMLButtonElement {
    return el("pr-submit-btn");
  }
  get prGenerateBtn(): HTMLButtonElement {
    return el("pr-generate-btn");
  }
  get prNewBtn(): HTMLButtonElement {
    return el("git-pr-new-btn");
  }

  // CI pill
  get ciPill(): HTMLButtonElement {
    return el("git-ci-pill");
  }
  get ciPanel(): HTMLDivElement {
    return el("git-ci-panel");
  }

  // Settings panel (extra getters added by api-client migration)
  get settingsSaveStatus(): HTMLSpanElement {
    return el("settings-save-status");
  }
  get notifyToggle(): HTMLInputElement {
    return el("notify-toggle");
  }
  get notifyHint(): HTMLParagraphElement {
    return el("notify-hint");
  }
  get notifySubOptions(): HTMLDivElement {
    return el("notify-sub-options");
  }
  get notifyFinishedToggle(): HTMLInputElement {
    return el("notify-finished-toggle");
  }
  get notifyPermissionToggle(): HTMLInputElement {
    return el("notify-permission-toggle");
  }

  // Settings tab bar (mobile dropdown + desktop segmented control)
  get settingsTabBar(): HTMLDivElement {
    return el("settings-tab-bar");
  }
  get settingsTabSelect(): HTMLSelectElement {
    return el("settings-tab-select");
  }

  // MCP modal (shared by add + edit)
  get mcpModal(): HTMLDivElement {
    return el("mcp-modal");
  }

  // Upload progress bar (shared UI at the bottom of viewport)
  get uploadProgress(): HTMLDivElement {
    return el("upload-progress");
  }
  get uploadProgressFill(): HTMLDivElement {
    return el("upload-progress-fill");
  }
  get uploadProgressLabel(): HTMLElement {
    return el("upload-progress-label");
  }
  get uploadProgressCancel(): HTMLButtonElement {
    return el("upload-progress-cancel");
  }

  // Theme toggle
  get themeBtn(): HTMLButtonElement {
    return el("theme-btn");
  }

  // Subagent modal
  get subagentModalTitle(): HTMLElement {
    return el("subagent-modal-title");
  }
  get subagentModalBody(): HTMLPreElement {
    return el("subagent-modal-body");
  }

  // Tabs / shell
  get toolbarTitle(): HTMLElement {
    return el("toolbar-title");
  }

  // Startup
  get appRoot(): HTMLElement {
    return el("app");
  }
  get chatArea(): HTMLElement {
    return el("chat-area");
  }
}

export const $ = new Elements();

// --- View Transition helper ---

/** Wrap a DOM swap in a view transition if the browser supports it.
 *  Falls back to calling `fn()` directly. Catches ready/finished
 *  rejections (expected when the transition is skipped). */
export function maybeViewTransition(fn: () => void): void {
  if (document.startViewTransition) {
     
    const t = document.startViewTransition(fn);
    t.ready.catch(() => {
      /* noop */
    });
    t.finished.catch(() => {
      /* noop */
    });
  } else {
    fn();
  }
}
