// Canonical store.js mock for test files. Single source of truth for all store
// exports — add new exports here when store.ts gains them.
//
// The precedent is scroll-mock.ts beside this file, and the failure it answers
// is the same one: Browser Mode links real ESM, so a factory listing store
// exports by hand fails the WHOLE FILE the moment the store gains a name the
// graph imports ("does not provide an export named 'dropConfirmedSteers'").
// Three action suites had hand-rolled factories and all three died that way when
// the mid-turn-steer work landed five names at once.
//
// Spread it and override what a suite actually drives:
//
//   vi.mock("../store.js", async () => ({
//     ...(await import("../__test-helpers__/store-mock.js")).storeMock,
//     get: mockGet,
//   }));
//
// store-mock.test.ts is the drift guard: it fails when store.ts gains an export
// this object lacks, or keeps one store.ts no longer has.
import { vi } from "vitest";
import { signal, computed } from "@cplieger/reactive";

export const storeMock = {
  // The two reactive exports are REAL primitives rather than vi.fn()s, because
  // their consumers read `.value` and subscribe to them. A mock function there
  // is a crash waiting for the first suite whose graph wires an effect at import
  // time; a signal that never changes is inert instead.
  messagesVersion: signal(0),
  activeSession: computed<undefined>(() => undefined),
  emitMessages: vi.fn(),

  MODEL_CONTEXT_SIZES: {} as Record<string, number>,
  parseContextSize: vi.fn((): number | undefined => undefined),
  contextSizeFor: vi.fn(() => 0),
  defaultUsage: vi.fn(() => ({})),

  getSessions: vi.fn(() => []),
  getActiveId: vi.fn(() => ""),
  getActive: vi.fn((): undefined => undefined),
  get: vi.fn((): undefined => undefined),
  setSessions: vi.fn(),
  setActive: vi.fn(),
  // The head of the list, not the real function's -1: a suite reaching this is
  // on a path that feeds the index straight back to `reinsertSession`, and -1
  // there is not a position. Override it when the index itself is the subject.
  indexOfSession: vi.fn(() => 0),
  reinsertSession: vi.fn(),
  removeChat: vi.fn(),
  upsertHeader: vi.fn(),

  isThinking: vi.fn(() => false),
  isEmptyChat: vi.fn(() => false),
  setThinking: vi.fn(),
  setTurnFailed: vi.fn(),
  clearTurnFailed: vi.fn(),
  setTurnDone: vi.fn(),
  clearTurnDone: vi.fn(),
  setTurnSummary: vi.fn(),

  tabStatusFor: vi.fn(() => ""),
  runStatusFor: vi.fn(() => ""),
  setAgentStatus: vi.fn(),
  setWorkingLabel: vi.fn(),

  // Mirrors `internal/vibekit`'s derived id, so a suite that spreads this and
  // asserts on a steer id gets the real shape rather than a placeholder.
  steerIDFor: vi.fn((messageID: string) => `steer-${messageID}`),
  steerCount: vi.fn(() => 0),
  steerMarks: vi.fn(() => []),
  recordSteerSent: vi.fn(),
  recordSteerQueued: vi.fn(),
  promoteSteer: vi.fn(),
  forgetSteer: vi.fn(),
  forgetSteers: vi.fn(),
  dropSteers: vi.fn(),
  dropConfirmedSteers: vi.fn(() => []),
  restoreSteers: vi.fn(),

  rebuildMsgIndex: vi.fn(),
  normalizeMessage: vi.fn((m: unknown) => m),
  appendMessage: vi.fn(),
  upsertMessage: vi.fn(),
  appendChunk: vi.fn(),
  upsertToolCall: vi.fn(),
  setCodeReferences: vi.fn(),
  setSnapshotSeq: vi.fn(),
  clearSnapshotSeq: vi.fn(),

  setCurrentMode: vi.fn(),
  setSupervisedMode: vi.fn(),
  setEffort: vi.fn(),
  setModel: vi.fn(),
  setName: vi.fn(),
};
