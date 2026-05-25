// Tests for type-only utility extractors: ArgsOf, ResultOf, ActionFromDef.
// These are compile-time tests — if the types resolve to `never` or wrong
// types, TypeScript errors at compile time and the test file won't even
// load. Each it() block does a runtime no-op assertion to satisfy
// vitest's requireAssertions.
//
// Background: when these were defined with `unknown` in non-inferred
// positions (e.g. Action<infer T, unknown>), variance on contravariant
// callback fields caused them to resolve to `never` for concrete actions.
// Fixed by using `any` in those slots.

import { describe, it, expect } from "vitest";
import type { Action, ActionDefinition, ArgsOf, ResultOf, ActionFromDef } from "./types.js";

// Helper: assert two types are equal at compile time.
type AssertEqual<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;
function expectType<T extends true>(): void { void (null as unknown as T); }

describe("type utilities", () => {
  it("ArgsOf extracts TArgs from concrete Action", () => {
    type A = Action<{ chatID: string }, number>;
    expectType<AssertEqual<ArgsOf<A>, { chatID: string }>>();
    expect(true).toBe(true);
  });

  it("ArgsOf with void args resolves to void", () => {
    type A = Action<void, string>;
    expectType<AssertEqual<ArgsOf<A>, void>>();
    expect(true).toBe(true);
  });

  it("ArgsOf with primitive arg type", () => {
    type A = Action<string, boolean>;
    expectType<AssertEqual<ArgsOf<A>, string>>();
    expect(true).toBe(true);
  });

  it("ResultOf extracts TResult from concrete Action", () => {
    type A = Action<{ x: number }, { ok: boolean; data: number }>;
    expectType<AssertEqual<ResultOf<A>, { ok: boolean; data: number }>>();
    expect(true).toBe(true);
  });

  it("ResultOf with void result resolves to void", () => {
    type A = Action<{ id: string }, void>;
    expectType<AssertEqual<ResultOf<A>, void>>();
    expect(true).toBe(true);
  });

  it("ActionFromDef extracts Action from an ActionDefinition type", () => {
    type Def = ActionDefinition<{ id: string }, string>;
    type A = ActionFromDef<Def>;
    expectType<AssertEqual<ArgsOf<A>, { id: string }>>();
    expectType<AssertEqual<ResultOf<A>, string>>();
    expect(true).toBe(true);
  });

  it("ArgsOf returns never for non-Action types", () => {
    expectType<AssertEqual<ArgsOf<string>, never>>();
    expectType<AssertEqual<ArgsOf<{ foo: number }>, never>>();
    expect(true).toBe(true);
  });

  it("ResultOf returns never for non-Action types", () => {
    expectType<AssertEqual<ResultOf<number>, never>>();
    expectType<AssertEqual<ResultOf<null>, never>>();
    expect(true).toBe(true);
  });

  it("ActionFromDef returns never for non-ActionDefinition types", () => {
    expectType<AssertEqual<ActionFromDef<string>, never>>();
    expect(true).toBe(true);
  });
});
