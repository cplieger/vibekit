import { vi } from "vitest";

export type SSEHandler = (chatID: string, payload: unknown) => void;
export const sseHandlers = new Map<string, SSEHandler>();

export function fireSSE(event: string, chatID: string, payload: unknown): void {
  const handler = sseHandlers.get(event);
  if (handler) {
    handler(chatID, payload);
  }
}

export function createBusMock(extras: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    onSSE: vi.fn((event: string, handler: SSEHandler) => {
      sseHandlers.set(event, handler);
    }),
    ...extras,
  };
}
