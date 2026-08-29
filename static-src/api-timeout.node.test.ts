import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { API_TIMEOUT_MS } from "@cplieger/fetch";

// The cross-language half of the admission-wait bound. The Go side
// (internal/command/prompt_admission_test.go) asserts AdmissionWait stays
// below the client API timeout, reading the value from this shared fixture;
// this side asserts the fixture equals the INSTALLED library's constant. A
// fetch release that moves API_TIMEOUT_MS fails here, forcing a fixture
// update, which the Go assertion then re-checks — no hand-maintained mirror.
const FIXTURE_PATH = "../internal/command/testdata/client_api_timeout.json";

describe("client API timeout fixture", () => {
  it("matches the installed @cplieger/fetch API_TIMEOUT_MS", () => {
    const fixture = JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as Record<string, unknown>;
    expect(fixture["api_timeout_ms"]).toBe(API_TIMEOUT_MS);
  });
});
