/**
 * Canonical toast mock factory. Use with vi.mock("../toast.js", toastMock).
 * Centralizes the mock shape so adding new toast exports only requires one update.
 */
import { vi } from "vitest";

export const toastMock = () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
});
