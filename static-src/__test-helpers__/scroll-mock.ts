// Canonical scroll.js mock for test files. Single source of truth for all
// scroll exports — add new exports here when scroll.ts gains them.
import { vi } from "vitest";
// Type-only, so this does NOT pull the module being mocked in at runtime. It is
// what lets a suite drive the reading state (`readingState.mockReturnValue`) with
// the other member of the union: inferred from the default alone, the mock's
// return type would be the literal "following" and "reading" would not typecheck.
import type { ReadingState, ShiftKind, ViewScrollState } from "../scroll.js";

export const scrollMock = {
  // The real VALUE, not a placeholder: `readingLineOffset` is mocked to 0, so any
  // suite reading this fraction is doing its own arithmetic and wants the number
  // the scroller actually publishes.
  READING_LINE_FRACTION: 1 / 3,
  getScrollEl: vi.fn(() => document.createElement("div")),
  // The multiplexer's park/unpark pair: detach snapshots the outgoing view's
  // scroll state, attach re-roots the observers on the incoming view. The
  // default snapshot is a fresh view's state so a mocked park/unpark cycle
  // round-trips without a suite having to prime it.
  attach: vi.fn(),
  detach: vi.fn((): ViewScrollState => ({ scrollTop: 0, readingState: "following" })),
  scrollToBottom: vi.fn(),
  setUserScrolledUp: vi.fn(),
  jumpTo: vi.fn(),
  // The self-scroll epoch. `scrollToOffset` moves nothing here, so a suite
  // asserting a LANDING drives the scroller itself.
  beginSelfScroll: vi.fn(),
  endSelfScroll: vi.fn(),
  scrollToOffset: vi.fn(),
  atLiveEdgeNow: vi.fn(() => true),
  // 0 is a REAL answer, not a placeholder: a suite asserting a landing against the
  // reading line has to prime it, or the two coincide and the assertion holds for
  // any arithmetic.
  readingLineOffset: vi.fn(() => 0),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  readingState: vi.fn((): ReadingState => "following"),
  onReadingStateChange: vi.fn(),
  // Inert registration: nothing in a mocked scroller mutates, so the callback
  // never fires. Returns the unregister the real hook contract promises.
  onTranscriptMutate: vi.fn(() => () => undefined),
  // Same shape, same reason: a mocked scroller produces no reader gesture, so a
  // suite that needs one drives the registered callback itself.
  onReaderGesture: vi.fn(() => () => undefined),
  // Inert registration: a mocked scroller emits no scroll, so the callback never
  // fires. Returns the unregister the real hook contract promises, like
  // `onTranscriptMutate` — so no mock-using suite can exercise a window pass.
  onViewportChange: vi.fn(() => () => undefined),
  // Same shape and the same reason: a mocked scroller resizes nothing, so the
  // callback never fires, and the return is the unregister the hook promises.
  onContentResize: vi.fn(() => () => undefined),
  // Same shape: no view parks or unparks against a mocked scroller.
  onAttach: vi.fn(() => () => undefined),
  setAnchorProvider: vi.fn(),
  setResumeLabel: vi.fn(),
  // The compensation helpers run their mutation, so a mocked scroll module does
  // not silently skip the DOM change the caller was making.
  //
  // `kind` is declared even though nothing here reads it, because the real export
  // takes it: without it the spy's call tuple is length 1 and a suite asserting
  // WHICH shift a caller declared does not typecheck.
  preserveReadingPosition: vi.fn((mutate: () => void, _kind: ShiftKind) => {
    mutate();
  }),
  deferWhileReading: vi.fn((mutate: () => void) => {
    mutate();
  }),
  fillViewport: vi.fn(),
  // How much room the transcript has left to scroll. Defaults to a comfortably
  // navigable value rather than 0, because the turn rail hides itself below
  // MIN_SCROLL_PX — a 0 here would silently withdraw the rail from every suite
  // that renders one and assert nothing about why.
  scrollableBy: vi.fn(() => 500),
};
