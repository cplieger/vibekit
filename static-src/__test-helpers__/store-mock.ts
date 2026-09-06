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
import { computed, SignalMap, type Signal } from "@cplieger/reactive";

import type { Session } from "../types.js";

// A REAL SignalMap rather than a vi.fn(), because consumers read the returned
// signal's `.value` and subscribe to it; a mock function there is a crash
// waiting for the first suite whose graph wires an effect at import time.
const versionSigs = new SignalMap<number>();

export const storeMock = {
  // The reactive exports are REAL primitives rather than vi.fn()s, because
  // their consumers read `.value` and subscribe to them. A mock function there
  // is a crash waiting for the first suite whose graph wires an effect at import
  // time; a signal that never changes is inert instead.
  messagesVersionOf: (chatID: string): Signal<number> => versionSigs.ensure(chatID, 0),
  activeSession: computed<undefined>(() => undefined),
  bumpMessages: vi.fn(),
  watchActiveId: vi.fn(() => ""),
  // `shape` is the real module's answer for a chat with no flushed cause, and
  // the full-pass default keeps a spreading suite on the pre-cause paint path.
  renderCauseOf: vi.fn(() => ({ cause: "shape" as const })),

  MODEL_CONTEXT_SIZES: {} as Record<string, number>,
  parseContextSize: vi.fn((): number | undefined => undefined),
  contextSizeFor: vi.fn(() => 0),
  defaultUsage: vi.fn(() => ({})),

  getSessions: vi.fn(() => []),
  getActiveId: vi.fn(() => ""),
  getActive: vi.fn((): undefined => undefined),
  get: vi.fn((): undefined => undefined),
  watchSession: vi.fn((): undefined => undefined),
  setSessions: vi.fn(),
  setActive: vi.fn(),
  // The head of the list, not the real function's -1: a suite reaching this is
  // on a path that feeds the index straight back to `reinsertSession`, and -1
  // there is not a position. Override it when the index itself is the subject.
  indexOfSession: vi.fn(() => 0),
  reinsertSession: vi.fn(),
  removeChat: vi.fn(),
  upsertHeader: vi.fn(),

  // The in-flight turn marker: which message id the chat file cannot carry yet.
  // `hasMessage` is the acceptance test a failed send reads before handing text
  // back to the composer, so it defaults to false — a suite that drives the
  // rescue path overrides it from its own array.
  hasMessage: vi.fn(() => false),
  noteLiveTurnMessage: vi.fn(),
  clearLiveTurnMessage: vi.fn(),
  liveTurnMessage: vi.fn(() => undefined),

  isThinking: vi.fn(() => false),
  isEmptyChat: vi.fn(() => false),
  setThinking: vi.fn(),
  setTurnOpen: vi.fn(),
  // The real derivation, like `steerIDFor` and `normalizeMessage`: it is a pure
  // function of the session handed to it, so there is no store state to fake and a
  // flat `false` would answer for a session that says otherwise. It also keeps a
  // spreading suite on the behaviour it was written against — the transcript's
  // liveness input used to be `session.thinking` read at the call site.
  turnLive: vi.fn((s: Session) => s.thinking || s.turn_open === true),
  setTurnFailed: vi.fn(),
  clearTurnFailed: vi.fn(),
  setTurnDone: vi.fn(),
  clearTurnDone: vi.fn(),
  relatchTurnVerdict: vi.fn(),
  // The outcome-to-latch table and the header-derived seed. Both answer the
  // EMPTY value for their type, like every other reader here: a mock that
  // latched `done` would make a dot assertion pass for a reason production did
  // not supply. A suite that drives the seed overrides it.
  outcomeLatch: vi.fn((): "done" | "failed" | "" => ""),
  latchFieldsFor: vi.fn((): { turn_done?: true; turn_failed?: true } => ({})),
  setTurnSummary: vi.fn(),

  tabStatusFor: vi.fn(() => ""),
  runStatusFor: vi.fn(() => ""),
  subagentStatusFor: vi.fn(() => ""),
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

  // The eviction surface. The two constants are the real values (a suite
  // reasoning about cadence should reason about the shipped numbers), the
  // registration returns a real unregister so composition-shaped code can
  // wire and unwind, and the rest are inert.
  EVICT_SWEEP_MS: 5 * 60 * 1000,
  EVICT_IDLE_MS: 30 * 60 * 1000,
  registerEvictionExemption: vi.fn(() => () => {}),
  startEvictionSweep: vi.fn(),
  stopEvictionSweep: vi.fn(),
  evictChatMessages: vi.fn(),

  // The staleness gate. `transcriptStale` defaults TRUE — the always-refetch
  // behavior every spreading suite was written against; a suite exercising the
  // zero-fetch activation overrides it.
  syncEpoch: vi.fn(() => 0),
  bumpSyncEpoch: vi.fn(),
  transcriptStale: vi.fn(() => true),
};
