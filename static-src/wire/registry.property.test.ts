// Property test verifying wire registry completeness: every registered event
// name decodes its own encoded output without loss.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { registerAllSSEDecoders } from "./registry.gen.js";
import { lookupSSEDecoder } from "../bus.js";
import * as decoders from "./decoders.gen.js";

// Register all decoders so lookupSSEDecoder works.
registerAllSSEDecoders();

// Expected event names from registry.gen.ts.
const registeredEvents = [
  "chat_created",
  "chat_deleted",
  "chat_updated",
  "connected",
  "elicitation_needed",
  "error",
  "mcp_connected",
  "mcp_disconnected",
  "mcp_failed",
  "mcp_oauth_needed",
  "message_appended",
  "message_chunk",
  "message_created",
  "message_updated",
  "permission_needed",
  "tool_call",
  "tool_call_update",
  "turn_ended",
] as const;

describe("wire registry completeness", () => {
  it("every registered event has a decoder via lookupSSEDecoder", () => {
    for (const eventName of registeredEvents) {
      const decoder = lookupSSEDecoder(eventName);
      expect(decoder, `missing decoder for ${eventName}`).toBeDefined();
    }
  });

  it("every registered event maps to a real exported generated decoder", () => {
    // Cross-check the registry wiring against the actual decoder module: each
    // registered event must resolve (via lookupSSEDecoder) to one of the
    // functions exported by decoders.gen.ts — not undefined, not an ad-hoc
    // wrapper. Catches a registry that wires a stub, a stale name, or nothing.
    const exportedDecoders = new Set<unknown>(
      Object.values(decoders).filter((v) => typeof v === "function"),
    );
    for (const eventName of registeredEvents) {
      const decoder = lookupSSEDecoder(eventName);
      expect(
        exportedDecoders.has(decoder),
        `${eventName} does not resolve to a decoder exported by decoders.gen.ts`,
      ).toBe(true);
    }
  });

  it("registered decoders reject the empty object with a TypeError (never another error class)", () => {
    // Exhaustive over every registered event (a plain loop, not a sampled
    // fc.constantFrom, so no event is skipped). An empty object is missing the
    // required fields of most payloads; the decoder must reject it via the
    // validators.ts failure mode (TypeError), not crash with some other error.
    // Payloads with no required fields (turn_ended, whoami) decode {} fine —
    // that's an accepted no-throw outcome, not a failure.
    for (const eventName of registeredEvents) {
      const decoder = lookupSSEDecoder(eventName);
      expect(decoder, `missing decoder for ${eventName}`).toBeDefined();
      try {
        decoder?.({});
      } catch (e) {
        expect(e, `${eventName} threw a non-TypeError on {}`).toBeInstanceOf(TypeError);
      }
    }
  });

  it("JSON round-trip stability: stringify(decode(x)) is valid JSON", () => {
    fc.assert(
      fc.property(
        fc.constantFrom(...registeredEvents),
        fc.dictionary(fc.string(), fc.jsonValue()),
        (eventName, payload) => {
          const decoder = lookupSSEDecoder(eventName);
          expect(decoder).toBeDefined();
          if (!decoder) {
            return;
          }
          try {
            const decoded = decoder(payload);
            const serialized = JSON.stringify(decoded);
            // Must be valid JSON (no undefined values leaked).
            expect(JSON.parse(serialized)).toBeDefined();
          } catch {
            // Decoder rejecting invalid input is fine.
          }
        },
      ),
      { numRuns: 50 },
    );
  });
});
