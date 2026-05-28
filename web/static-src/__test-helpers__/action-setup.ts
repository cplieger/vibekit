/**
 * Shared action-test boilerplate. Import and call setupActionTest() in
 * beforeEach to reset the action framework and stub fetch.
 *
 * NOTE: vi.mock() calls must remain at the top of each test file due to
 * Vitest hoisting. This helper provides the reset + stub logic only.
 */
import { vi } from "vitest";

import { _resetForTest as resetDefine } from "../actions/define.js";
import { _resetForTest as resetRegistry } from "../actions/registry.js";
import { _resetForTest as resetCleanup } from "../actions/cleanup.js";

/** The shared mock fetch instance. Stub this in your test file. */
export const mockFetch = vi.fn();

/**
 * Resets the action framework (define, registry, cleanup) and stubs
 * global fetch. Call in beforeEach().
 */
export function setupActionTest(): void {
  resetDefine();
  resetRegistry();
  resetCleanup();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
}

/** Canonical toast mock shape for vi.mock("../toast.js", ...) */
export const toastMockFactory = () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
});

/** Canonical api-client mock shape for vi.mock("../api-client.js", ...) */
export const apiClientMockFactory = () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) =>
    signal ?? new AbortController().signal,
});
