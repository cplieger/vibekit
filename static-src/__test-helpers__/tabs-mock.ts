// ---------------------------------------------------------------------------
// A COMPLETE `tabs.js` mock, for the many test files that mock the tab store to
// keep it out of their subject's way.
//
// Why this exists rather than each file listing the three or four names it cares
// about: Browser Mode links ESM for real rather than reading properties off a
// namespace object, so every name ANY module in a test's import graph reaches has
// to exist on the mock, including names that file never calls. A partial factory
// therefore works until the graph widens, and then breaks in a way that does not
// name what went wrong. The tab projection widened it for a dozen files at once,
// and the symptom was a browser page that closed with "rpc is closed" rather than
// a missing-export error, because a sibling mock's `importOriginal()` threw while
// resolving the broken link and took the page down with it.
//
// So: this is the whole surface, inert, in one place. A file spreads it and
// overrides only what it needs to observe:
//
//   vi.mock("./tabs.js", async () => ({
//     ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
//     activateTab: mockActivateTab,
//   }));
//
// When `tabs.ts` gains an export, this file is the one place that has to learn
// about it, and `tabs-mock.test.ts` fails until it does.
// ---------------------------------------------------------------------------

import { vi } from "vitest";

/** Every value `tabs.ts` exports, inert.
 *
 *  The readers return the empty answer for their type rather than a plausible
 *  one: a mock that claims a tab is open is a mock that makes a test pass for a
 *  reason the production code did not supply. The async mutators resolve rather
 *  than reject, because a caller awaiting one is exercising its own continuation,
 *  not the store's failure path. */
export function tabsMock(): Record<string, unknown> {
  return {
    // Mutators. Async since the projection made an open a server round trip.
    // openTab answers its OUTCOME type's success value, because a mock that
    // reports "failed" would send callers down their failure branches.
    openTab: vi.fn(async () => "opened"),
    closeTab: vi.fn(async () => {}),
    setTabPinned: vi.fn(async () => {}),
    openEditorView: vi.fn(async () => {}),
    openRunTab: vi.fn(async () => {}),
    openSubagentTab: vi.fn(async () => {}),
    toggleSettingsView: vi.fn(async () => {}),
    toggleGitView: vi.fn(async () => {}),
    toggleFilesView: vi.fn(async () => {}),
    showFilesView: vi.fn(async () => {}),
    toggleHistoryView: vi.fn(async () => {}),
    toggleDocsView: vi.fn(async () => {}),

    // Synchronous local writes. The three sub-tab setters stay synchronous on
    // purpose: a singleton's sub-tab is not part of the shared subject, so they
    // are the correction channel rather than a mutation.
    adoptSubject: vi.fn(),
    activateTab: vi.fn(),
    activateRestoredTab: vi.fn(),
    renameTab: vi.fn(),
    setTabStatus: vi.fn(),
    setTabDirty: vi.fn(),
    setTabTooltip: vi.fn(),
    setSettingsTab: vi.fn(),
    setGitTab: vi.fn(),
    setDocsTab: vi.fn(),

    // Readers, each answering "nothing".
    hasTab: vi.fn(() => false),
    tabIdFor: vi.fn(() => ""),
    tabSetVersion: vi.fn(() => 0),
    tabIdForRoute: vi.fn(() => ""),
    getActiveTabId: vi.fn(() => ""),
    getActiveTabRoute: vi.fn(() => null),
    getActiveTabKind: vi.fn(() => null),
    activeChatRef: vi.fn(() => ""),
    parentChatRef: vi.fn(() => ""),
    openChatRefs: vi.fn(() => []),
    openSubagentRefs: vi.fn(() => []),
    cueCandidates: vi.fn(() => []),

    // Registration slots. `subscribeTabCues` hands back its unsubscribe so a
    // caller's cleanup does not throw on undefined.
    subscribeTabCues: vi.fn(() => () => {}),
    setOnTabClosed: vi.fn(),
    setOnEmpty: vi.fn(),

    _resetForTest: vi.fn(),
  };
}
