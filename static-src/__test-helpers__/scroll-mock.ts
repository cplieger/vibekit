// Canonical scroll.js mock for test files. Single source of truth for all
// scroll exports — add new exports here when scroll.ts gains them.
import { vi } from "vitest";

export const scrollMock = {
  scroll: vi.fn(),
  getScrollEl: vi.fn(() => document.createElement("div")),
  scrollEl: null as HTMLElement | null,
  scrollToBottom: vi.fn(),
  suppressScroll: vi.fn(),
  setUserScrolledUp: vi.fn(),
  trimOldMessages: vi.fn(),
  resetScrollState: vi.fn(),
  setLoadMore: vi.fn(),
};
