import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import {
  classifyRunNodeStatus,
  classifyRunStatus,
  runStatusActive,
  runStatusTerminal,
} from "./run-status.js";
import { inFlight, stateOf, STATE_WORD, type ExecState } from "./exec-view/status.js";

interface StatusRow {
  status: string;
  active: boolean;
  terminal: boolean;
  exec_state?: ExecState;
}

interface StatusFixture {
  runs: StatusRow[];
  nodes: StatusRow[];
}

const raw = readFileSync(
  new URL("../internal/vibekit/testdata/run_statuses.json", import.meta.url),
  "utf8",
);
const fixture = JSON.parse(raw) as StatusFixture;

describe("the run-status contract shared with Go", () => {
  it.each(fixture.runs.map((row) => [row.status, row] as const))("classifies %s", (_name, row) => {
    const status = classifyRunStatus(row.status);
    expect(status).toBe(row.status);
    expect(status === undefined ? undefined : runStatusActive(status)).toBe(row.active);
    expect(status === undefined ? undefined : runStatusTerminal(status)).toBe(row.terminal);
  });

  it("keeps an unknown run live and free of a guessed ending", () => {
    const status = classifyRunStatus("quiesced");
    expect(status).toBe("unknown");
    expect(status === undefined ? undefined : runStatusActive(status)).toBe(true);
    expect(status === undefined ? undefined : runStatusTerminal(status)).toBe(false);
  });
});

describe("the node-status contract shared with Go", () => {
  it.each(fixture.nodes.map((row) => [row.status, row] as const))("classifies %s", (_name, row) => {
    const status = classifyRunNodeStatus(row.status);
    expect(status).toBe(row.status);
    expect(stateOf(status)).toBe(row.exec_state);
  });

  it("keeps an unknown node live and distinct from not started", () => {
    const state = stateOf(classifyRunNodeStatus("blocked"));
    expect(state).toBe("unknown");
    expect(inFlight(state)).toBe(true);
    expect(STATE_WORD[state]).toBe("unknown");
  });
});
