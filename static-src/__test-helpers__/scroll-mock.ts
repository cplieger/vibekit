// Canonical scroll.js mock for test files. Single source of truth for all
// scroll exports — add new exports here when scroll.ts gains them.
import { vi } from "vitest";
// Type-only, so this does NOT pull the module being mocked in at runtime. It is
// what lets a suite drive the reading state (`readingState.mockReturnValue`) with
// the other member of the union: inferred from the default alone, the mock's
// return type would be the literal "following" and "reading" would not typecheck.
import type { ReadingState, ViewScrollState } from "../scroll.js";

export const scrollMock = {
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
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
  readingState: vi.fn((): ReadingState => "following"),
  onReadingStateChange: vi.fn(),
  // Inert registration: nothing in a mocked scroller mutates, so the callback
  // never fires. Returns the unregister the real hook contract promises.
  onTranscriptMutate: vi.fn(() => () => undefined),
  // Inert registration: a mocked scroller emits no scroll, so the callback never
  // fires. Returns the unregister the real hook contract promises, like
  // `onTranscriptMutate` — so no mock-using suite can exercise a window pass.
  onViewportChange: vi.fn(() => () => undefined),
  setAnchorProvider: vi.fn(),
  setResumeLabel: vi.fn(),
  // The compensation helpers run their mutation, so a mocked scroll module does
  // not silently skip the DOM change the caller was making.
  preserveReadingPosition: vi.fn((mutate: () => void) => {
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
