import { vi } from "vitest";

type SSEHandler = (chatID: string, payload: unknown) => void;
const sseHandlers = new Map<string, SSEHandler>();

export function fireSSE(event: string, chatID: string, payload: unknown): void {
  const handler = sseHandlers.get(event);
  if (handler) {
    handler(chatID, payload);
  }
}

export function createBusMock(extras: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    // bus.ts's full named surface. Browser Mode links ESM for real, so a name
    // another module in the graph imports has to EXIST on the mock or the whole
    // import fails; a namespace-object property read (which is what the node
    // runner did) tolerated its absence. `undefined` is what that read produced,
    // so nothing a caller exercises changes — a path that reaches one of these
    // failed before and fails now.
    dispatch: undefined,
    registerSSEDecoder: undefined,
    lookupSSEDecoder: undefined,
    onBus: undefined,
    emitBus: undefined,
    BUS_TURN_IDLE: undefined,
    BUS_TRANSPORT_GAP: undefined,
    BUS_KEYS_ESCAPE: undefined,
    BUS_ACTIVATE_CHAT: undefined,
    BUS_RUNS_CHANGED: undefined,
    BUS_TAB_CHANGED: undefined,
    onSSE: vi.fn((event: string, handler: SSEHandler) => {
      sseHandlers.set(event, handler);
    }),
    ...extras,
  };
}
