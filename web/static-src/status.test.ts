import { describe, it, expect } from "vitest";
import { formatTokens, formatMetering } from "./status-format.js";

describe("formatTokens", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.0M"],
    [1_500_000, "1.5M"],
    [10_000_000, "10.0M"],
  ];

  it.each(cases)("formatTokens(%d) === %s", (input, expected) => {
    expect(formatTokens(input)).toBe(expected);
  });
});

describe("formatMetering", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [42, "42"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.00M"],
    [1_500_000, "1.50M"],
    [0.5, "0.50"],
    [3.14, "3.14"],
    [99.9, "99.90"],
  ];

  it.each(cases)("formatMetering(%d) === %s", (input, expected) => {
    expect(formatMetering(input)).toBe(expected);
  });
});
