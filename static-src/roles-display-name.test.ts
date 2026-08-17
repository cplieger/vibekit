// Mode names that are really identifiers, spaced for display.
//
// The role list showed "Semantic_reviewer" beside eleven properly-spaced
// neighbours. The six bundled workflow modes carry hand-written names, so the
// underscore only ever showed on the sources that hand over a raw id: KAS names
// its non-workflow agents after themselves, and a workspace agent is named by
// its front matter, commonly the file's snake_case stem.
//
// The rule is by SHAPE, not by a list of ids, so the interesting half of this
// suite is the NEGATIVE half: a name a human wrote must survive untouched. Both
// marks the rule keys on (whitespace, a capital) are things the transformation
// would destroy, which is why they are the marks.
import { describe, it, expect } from "vitest";
import { displayModeName, labelForMode } from "./roles.js";
import type { SessionMode } from "./types.js";

describe("displayModeName", () => {
  it("spaces and capitalises an identifier-shaped name", () => {
    expect(displayModeName("semantic_reviewer")).toBe("Semantic Reviewer");
    expect(displayModeName("code_reviewer_v2")).toBe("Code Reviewer V2");
    expect(displayModeName("bug-fix")).toBe("Bug Fix");
    expect(displayModeName("vibe")).toBe("Vibe");
  });

  it("leaves a name a human wrote alone", () => {
    // A space is the clearest mark of authorship, and it is also what the
    // transformation produces — so a name that already has one is already done.
    expect(displayModeName("Bug Fix")).toBe("Bug Fix");
    expect(displayModeName("code reviewer")).toBe("code reviewer");
    // A capital anywhere means someone chose the casing. Rewriting "iOS" to
    // "Ios" is the failure this half exists to prevent.
    expect(displayModeName("iOS review")).toBe("iOS review");
    expect(displayModeName("iOS")).toBe("iOS");
    expect(displayModeName("McpAudit")).toBe("McpAudit");
  });

  it("does not invent a name for an empty one", () => {
    expect(displayModeName("")).toBe("");
  });

  it("survives separators that would otherwise yield empty words", () => {
    // A trailing or doubled separator must not produce " Foo" or "Foo  Bar";
    // an id like this is unlikely but a filename stem can carry one.
    expect(displayModeName("__odd__name__")).toBe("Odd Name");
    expect(displayModeName("a..b")).toBe("A B");
    expect(displayModeName("_")).toBe("");
  });
});

describe("labelForMode", () => {
  const modes: readonly SessionMode[] = [
    { id: "vibe", name: "Default", description: "", source: "bundled" },
    { id: "semantic_reviewer", name: "semantic_reviewer", description: "", source: "bundled" },
  ];

  it("spaces a wire name that is an identifier", () => {
    expect(labelForMode("semantic_reviewer", modes)).toBe("Semantic Reviewer");
  });

  it("leaves a hand-written wire name alone", () => {
    expect(labelForMode("vibe", modes)).toBe("Default");
  });

  it("spaces the raw-id fallback too", () => {
    // Nothing in the catalog matches, so the id itself becomes the label. It is
    // the same shape as a wire name and gets the same treatment; before this,
    // the fallback was the other way an underscore reached the pill.
    expect(labelForMode("my_custom_agent", modes)).toBe("My Custom Agent");
  });
});
