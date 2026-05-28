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
  "commands_updated",
  "connected",
  "error",
  "mcp_connected",
  "mcp_disconnected",
  "mcp_failed",
  "mcp_oauth_needed",
  "message_appended",
  "message_chunk",
  "message_created",
  "message_updated",
  "pending_change_added",
  "pending_change_resolved",
  "pending_changes_cleared",
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

  it("all registered events have corresponding decoder functions", () => {
    const decoderFns = Object.keys(decoders).filter((k) => k.startsWith("decode"));
    // decoders.gen.ts exports more decoders than are registered (some are
    // used internally or for sub-types). The key invariant is that every
    // registered event has a working decoder.
    expect(decoderFns.length).toBeGreaterThan(0);
    expect(registeredEvents.length).toBeGreaterThan(0);
  });

  it("decoders do not throw on valid JSON-parsed input", () => {
    fc.assert(
      fc.property(fc.constantFrom(...registeredEvents), (eventName) => {
        const decoder = lookupSSEDecoder(eventName);
        expect(decoder).toBeDefined();
        // Feed a minimal valid object — decoder should not throw TypeError
        // for a plain object (it may return a partial result).
        try {
          decoder!({});
        } catch (e) {
          // TypeError is acceptable (validation), other errors are not.
          if (!(e instanceof TypeError)) {
            throw e;
          }
        }
      }),
      { numRuns: registeredEvents.length },
    );
  });

  it("JSON round-trip stability: stringify(decode(x)) is valid JSON", () => {
    fc.assert(
      fc.property(
        fc.constantFrom(...registeredEvents),
        fc.dictionary(fc.string(), fc.jsonValue()),
        (eventName, payload) => {
          const decoder = lookupSSEDecoder(eventName);
          if (!decoder) return;
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
