/**
 * Shared action-test setup. Provides resetActionFramework() and mock factories.
 *
 * NOTE: vi.mock() calls must remain at the top of each test file (Vitest hoisting).
 * Use the exported factory functions as the mock implementation argument.
 */
import { vi } from "vitest";

import { _resetForTest as resetDefine } from "../define.js";
import { _resetForTest as resetRegistry } from "../registry.js";
import { _resetForTest as resetCleanup } from "../cleanup.js";

/** Resets define, registry, and cleanup modules. Call in beforeEach(). */
export function resetActionFramework(): void {
  resetDefine();
  resetRegistry();
  resetCleanup();
}

/** Canonical toast mock factory for vi.mock("../toast.js", mockToast) */
export const mockToast = () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
});
