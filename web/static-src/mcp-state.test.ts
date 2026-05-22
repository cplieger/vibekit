// Unit tests for mcp-state.ts adaptStatus wire-to-domain mapping.
import { describe, it, expect } from "vitest";
import { adaptStatus } from "./mcp-state.js";

describe("adaptStatus", () => {
  const cases = [
    {
      name: "connected state preserves name",
      input: { name: "github", state: "connected" },
      expected: { name: "github", state: "connected" },
    },
    {
      name: "needs_auth with oauth_url preserves url",
      input: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
      expected: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
    },
    {
      name: "needs_auth without oauth_url defaults to empty string",
      input: { name: "sentry", state: "needs_auth" },
      expected: { name: "sentry", state: "needs_auth", oauth_url: "" },
    },
    {
      name: "failed with error preserves error",
      input: { name: "pg", state: "failed", error: "connection refused" },
      expected: { name: "pg", state: "failed", error: "connection refused" },
    },
    {
      name: "failed without error defaults to empty string",
      input: { name: "redis", state: "failed" },
      expected: { name: "redis", state: "failed", error: "" },
    },
    {
      name: "idle state",
      input: { name: "slack", state: "idle" },
      expected: { name: "slack", state: "idle" },
    },
    {
      name: "unknown state falls through to idle",
      input: { name: "custom", state: "reconnecting" },
      expected: { name: "custom", state: "idle" },
    },
    {
      name: "empty name preserved as-is",
      input: { name: "", state: "connected" },
      expected: { name: "", state: "connected" },
    },
  ] as const;

  for (const { name, input, expected } of cases) {
    it(name, () => {
      expect(adaptStatus(input as Parameters<typeof adaptStatus>[0])).toEqual(expected);
    });
  }
});
