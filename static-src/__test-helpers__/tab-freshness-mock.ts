// Canonical tab-freshness.js mock. Single source of truth for all
// tab-freshness exports — add new exports here when the leaf gains them.
//
// Browser Mode links real ESM, so a factory listing the leaf's exports by hand
// fails the WHOLE file the moment the graph reaches a name it omits.
// tab-freshness-mock.test.ts is the drift guard.
import { vi } from "vitest";

// `viewStale` defaults TRUE — the always-refetch behaviour every spreading suite
// is written against, matching `store-mock`'s `transcriptStale`. A suite
// exercising the zero-fetch activation drives it with `mockReturnValue(false)`.
const viewStale = vi.fn((): boolean => true);

export const tabFreshnessMock = {
  syncEpoch: vi.fn(() => 0),
  bumpSyncEpoch: vi.fn(),
  noteLoaded: vi.fn(),
  forgetView: vi.fn(),
  forgetAllViews: vi.fn(),
  subjectKey: vi.fn(() => ""),
  viewStale,
  /** Real rather than a spy: a mocked leaf holds no ledger, so the only state to
   *  put back is the verdict a suite drove. */
  _resetForTest: (): void => {
    viewStale.mockReturnValue(true);
  },
};
