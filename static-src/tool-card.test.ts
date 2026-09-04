// Unit tests for tool-card.ts pure functions (extractSubtitle, mcpHue).
import { describe, it, expect, vi, beforeAll } from "vitest";
import fc from "fast-check";

// Provide minimal DOM elements that transitive imports require at module level.
beforeAll(() => {
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (!document.getElementById(id)) {
      const el = document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
});

// Mock scroll.ts to avoid its eager DOM access ($.messages at module level).
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Mock editor-openers.ts to avoid its transitive DOM dependencies.
const opened: string[] = [];
vi.mock("./editor-openers.js", () => ({
  openFile: (p: string) => {
    opened.push(`file:${p}`);
  },
  openFileDiff: (p: string) => {
    opened.push(`diff:${p}`);
  },
  openFileGitDiff: (p: string) => {
    opened.push(`gitdiff:${p}`);
  },
}));

// Mock tool-group.ts to avoid its transitive DOM dependencies.
vi.mock("./tool-group.js", () => ({
  trackInProgress: () => {
    /* noop */
  },
}));

const { extractSubtitle, mcpHue, buildToolCard } = await import("./tool-card.js");

// ---------------------------------------------------------------------------
// extractSubtitle — table-driven
// ---------------------------------------------------------------------------

describe("extractSubtitle", () => {
  const cases: {
    name: string;
    input: Record<string, unknown> | undefined;
    expected: string;
  }[] = [
    { name: "undefined input", input: undefined, expected: "" },
    { name: "empty object", input: {}, expected: "" },
    { name: "query key", input: { query: "find all files" }, expected: "find all files" },
    { name: "pattern key", input: { pattern: "*.ts" }, expected: "*.ts" },
    { name: "command key", input: { command: "ls -la" }, expected: "ls -la" },
    { name: "url key", input: { url: "https://example.com" }, expected: "https://example.com" },
    { name: "path key", input: { path: "/src/main.ts" }, expected: "/src/main.ts" },
    { name: "explanation key", input: { explanation: "doing stuff" }, expected: "doing stuff" },
    {
      name: "priority order: query wins over path",
      input: { path: "/a", query: "q" },
      expected: "q",
    },
    {
      name: "priority order: pattern wins over command",
      input: { command: "c", pattern: "p" },
      expected: "p",
    },
    { name: "empty string value skipped", input: { query: "", path: "/x" }, expected: "/x" },
    { name: "non-string value skipped", input: { query: 42, path: "/y" }, expected: "/y" },
    {
      name: "truncation at 121 chars",
      input: { query: "a".repeat(121) },
      expected: "a".repeat(117) + "\u2026",
    },
    {
      name: "exactly 120 chars not truncated",
      input: { query: "b".repeat(120) },
      expected: "b".repeat(120),
    },
    { name: "no matching keys", input: { foo: "bar", baz: "qux" }, expected: "" },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(extractSubtitle(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// mcpHue — table-driven snapshot tests
// ---------------------------------------------------------------------------

describe("mcpHue", () => {
  const knownServers: { server: string; hue: number }[] = [
    { server: "github", hue: mcpHue("github") },
    { server: "s3", hue: mcpHue("s3") },
    { server: "postgres", hue: mcpHue("postgres") },
    { server: "filesystem", hue: mcpHue("filesystem") },
    { server: "brave-search", hue: mcpHue("brave-search") },
  ];

  it.each(knownServers)("deterministic for $server → $hue", ({ server, hue }) => {
    // Call multiple times to verify determinism.
    expect(mcpHue(server)).toBe(hue);
    expect(mcpHue(server)).toBe(hue);
  });

  it("returns value in [0, 360) for empty string", () => {
    const result = mcpHue("");
    expect(result).toBeGreaterThanOrEqual(0);
    expect(result).toBeLessThan(360);
  });

  it("different inputs produce different hues (for known distinct servers)", () => {
    const hues = new Set(knownServers.map((s) => s.hue));
    // With 5 distinct server names, we expect at least 3 distinct hues.
    expect(hues.size).toBeGreaterThanOrEqual(3);
  });
});

// ---------------------------------------------------------------------------
// mcpHue — property-based tests
// ---------------------------------------------------------------------------

describe("mcpHue properties", () => {
  it("output is always in [0, 360)", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        const h = mcpHue(s);
        expect(h).toBeGreaterThanOrEqual(0);
        expect(h).toBeLessThan(360);
        expect(Number.isInteger(h)).toBe(true);
      }),
    );
  });

  it("deterministic: same input always yields same output", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        expect(mcpHue(s)).toBe(mcpHue(s));
      }),
    );
  });

  it("distribution: 100 random strings cover at least 3 of 4 quadrants", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 100, maxLength: 100 }),
        (strings) => {
          const quadrants = new Set<number>();
          for (const s of strings) {
            quadrants.add(Math.floor(mcpHue(s) / 90));
          }
          expect(quadrants.size).toBeGreaterThanOrEqual(3);
        },
      ),
    );
  });
});

// ---------------------------------------------------------------------------
// The depth ladder's visible contract. These are task E's own done-when
// criteria, and none of them had a test before: the status word had no coverage
// at all, which is how a card printing `completed` survived.
// ---------------------------------------------------------------------------

describe("outcome is the row's one mark, not a word and not a badge", () => {
  it("a finished card prints no status word anywhere in its text", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    for (const status of ["completed", "failed"] as const) {
      const card = buildToolCard({
        id: "t1",
        title: "strReplace",
        kind: "edit",
        status,
        input: { path: "src/a.ts", oldStr: "a", newStr: "b" },
        live: false,
      });
      // The literal wire enum must not appear as visible text.
      expect(card.textContent).not.toContain("completed");
      expect(card.textContent).not.toContain("failed");
      expect(card.querySelector(".tool-status")).toBeNull();
    }
  });

  it("keeps its identity glyph on success and REPLACES it on a failure", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const { toolIcon, outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const ok = buildToolCard({
      id: "t2",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const okIcon = ok.querySelector(".tool-icon");
    expect(okIcon?.classList.contains("is-ok")).toBe(true);
    // Success is the ROW's OWN glyph, tinted — not a general success mark. One
    // mark, and no badge beside it.
    const identity = okIcon?.querySelector("svg")?.outerHTML ?? "";
    expect(identity).toBe((iconEl(toolIcon("execute", "executePwsh")) as HTMLElement).outerHTML);
    expect(identity).not.toBe((iconEl(outcomeIcon("ok")) as HTMLElement).outerHTML);
    expect(okIcon?.querySelectorAll("svg")).toHaveLength(1);

    const bad = buildToolCard({
      id: "t3",
      title: "executePwsh",
      kind: "execute",
      status: "failed",
      live: false,
    });
    const badIcon = bad.querySelector(".tool-icon");
    expect(badIcon?.classList.contains("is-fail")).toBe(true);
    // Still ONE mark, and it is a different SHAPE — which is what keeps hue from
    // being the only channel (WCAG 1.4.1).
    expect(badIcon?.querySelectorAll("svg")).toHaveLength(1);
    expect(badIcon?.querySelector("svg")?.outerHTML).not.toBe(identity);
  });

  it("gives the four outcome states four distinct shapes", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const { outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const identity = buildToolCard({
      id: "t2d",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    }).querySelector(".tool-icon svg")?.outerHTML;
    // The shape channel: if two states ever resolved to one glyph, or a
    // silhouette collided with a row's identity glyph, this is what fails.
    const marks = [
      identity ?? "",
      ...(["ok", "fail", "warn", "denied"] as const).map(
        (s) => (iconEl(outcomeIcon(s)) as HTMLElement).outerHTML,
      ),
    ];
    expect(new Set(marks).size).toBe(marks.length);
  });

  it("carries the outcome word in the accessible name instead", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4",
      title: "strReplace",
      kind: "edit",
      status: "failed",
      input: { path: "src/auth.go", oldStr: "a", newStr: "b" },
      live: false,
    });
    expect(card.getAttribute("aria-label")).toContain("auth.go");
    expect(card.getAttribute("aria-label")).toContain("failed");
  });

  // `aborted` has no tool counterpart — it is a run-level status, admitted to
  // this vocabulary so the History page states a run's verdict through the same
  // writer instead of growing a second one.
  it("paints an aborted subject amber with the stop silhouette, not as a failure", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const { outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const card = buildToolCard({
      id: "t4a",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    applyOutcome(card, "aborted", "Open feature-pipeline", {
      kind: "other",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    });
    const icon = card.querySelector(".tool-icon");
    expect(icon?.classList.contains("is-warn")).toBe(true);
    // Stopped is not broken: the failure tint must be gone, not merely joined.
    expect(icon?.classList.contains("is-fail")).toBe(false);
    expect(icon?.classList.contains("is-ok")).toBe(false);
    // Amber is shared with a policy refusal, so the SHAPE is what separates them,
    // and it comes from the shared set rather than from a copy here.
    expect(icon?.querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("warn")) as HTMLElement).outerHTML,
    );
    // Rebuilt, not stacked: the identity glyph from the first paint is gone.
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    expect(card.dataset["outcome"]).toBe("warn");
    expect(card.getAttribute("aria-label")).toBe("Open feature-pipeline, aborted");
  });

  it("leaves exactly one svg in the slot however often applyOutcome is called", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4b",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const icon = card.querySelector(".tool-icon");
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    // Same state twice, then a change, then the same state again: the writer
    // REPLACES the slot's child rather than appending to it, so stacking two
    // marks in one glyph is unrepresentable.
    applyOutcome(card, "completed", "Run Command", info);
    applyOutcome(card, "completed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });

  it("restores the identity glyph when a failed card re-renders as ok", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const { toolIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const card = buildToolCard({
      id: "t4c",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const icon = card.querySelector(".tool-icon");
    // Named off the icon registry, not read back off the card: a snapshot taken
    // after buildToolCard has already been through applyOutcome once, so it would
    // move with any regression that made `ok` paint a general mark.
    const identity = (iconEl(toolIcon("execute", "executePwsh")) as HTMLElement).outerHTML;
    expect(icon?.querySelector("svg")?.outerHTML).toBe(identity);
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelector("svg")?.outerHTML).not.toBe(identity);
    // Back to ok: the glyph captured at build comes back, so the writer cannot
    // strand a surface on a silhouette it borrowed.
    applyOutcome(card, "completed", "Run Command", info);
    expect(icon?.querySelector("svg")?.outerHTML).toBe(identity);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });

  it("does not strand a glyph-less slot on the silhouette it borrowed", async () => {
    const { applyOutcome } = await import("./tool-card.js");
    // A caller that mounts no identity glyph. Every caller in the tree mounts one
    // before its first call, so this pins the contract for the next one rather
    // than a live path: the writer owns the silhouette it wrote, so a return to
    // `ok` must not leave a red triangle standing under an `is-ok` class.
    const node = document.createElement("div");
    const icon = document.createElement("span");
    icon.className = "tool-icon";
    node.appendChild(icon);
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    applyOutcome(node, "failed", "Run Command", info);
    expect(icon.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(node, "completed", "Run Command", info);
    expect(icon.querySelectorAll("svg")).toHaveLength(0);
    expect(icon.classList.contains("is-ok")).toBe(true);
    expect(node.getAttribute("aria-label")).toBe("Run Command, succeeded");
  });
});

describe("the depth ladder", () => {
  it("a claim-only kind gets no details region and no toggle", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t5",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).toBeNull();
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    // The CSS affordance hook tracks the toggle: no toggle, no class.
    expect(card.querySelector(".tool-summary")?.classList.contains("has-disclosure")).toBe(false);
  });

  it("an edit gets a details region — the old tier axis gave it none", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t6",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one\ntwo", newStr: "one\nTWO" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).not.toBeNull();
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    // ...and a toggle brings the affordance class with it (14-tools.css keys
    // cursor/hover on the whole summary).
    expect(card.querySelector(".tool-summary")?.classList.contains("has-disclosure")).toBe(true);
  });

  it("has no second View diff button — the subject is the link", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t7",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    expect(card.querySelector(".tool-diff-view-btn")).toBeNull();
    expect(card.textContent).not.toContain("View diff");
  });

  it("the filename opens the DIFF on a change and the FILE on a read", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    opened.length = 0;
    const edit = buildToolCard({
      id: "t8",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    edit.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["gitdiff:src/a.ts"]);

    opened.length = 0;
    const read = buildToolCard({
      id: "t9",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    read.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["file:src/a.ts"]);
  });

  it("a move states from and to, which its claim line cannot carry", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t10",
      title: "smartRelocate",
      kind: "move",
      status: "completed",
      input: { sourcePath: "old/a.ts", destinationPath: "new/a.ts" },
      live: false,
    });
    const row = card.querySelector(".tool-move-row");
    expect(row?.textContent).toContain("old/a.ts");
    expect(row?.textContent).toContain("new/a.ts");
  });
});

describe("the search-hit seam", () => {
  it("linkifies a search tool's output, which is where the hits are", async () => {
    // linkifyPaths ran on prose and turn headers but never on tool output, so a
    // grep result line — `path:line: match`, the whole point of the tool — was
    // plain text with nothing to click.
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "s1",
      title: "grepSearch",
      kind: "search",
      status: "completed",
      input: { query: "needle" },
      output: "src/a.ts:42: found the needle\n",
      live: false,
    });
    // The output is painted on first open, with the linkify pass in it.
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    const link = card.querySelector<HTMLElement>(".tool-output .inline-file-link");
    expect(link).not.toBeNull();
    expect(link?.textContent).toContain("a.ts:42");
    card.remove();
  });
});

describe("disclose_context and policy denials", () => {
  // The agent activating a skill is the moment its body enters the prompt, so
  // the card names the DOCUMENT rather than the tool that fetched it. This is
  // the only signal in the transcript that a skill reached the model at all.
  it("names the skill a disclose_context call loaded", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t1",
      title: "disclose_context",
      kind: "other",
      status: "completed",
      live: false,
      disclosed: { type: "skill", display_name: "code-review", uri: "file:///x/code-review" },
    });
    expect(card.dataset["title"]).toBe("Loaded skill: code-review");
    expect(card.dataset["disclosed"]).toBe("skill");
  });

  it("distinguishes a steering document from a skill", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t2",
      title: "disclose_context",
      kind: "other",
      status: "completed",
      live: false,
      disclosed: { type: "steering", display_name: "vibekit", uri: "file:///x/vibekit.md" },
    });
    expect(card.dataset["title"]).toBe("Loaded steering: vibekit");
  });

  // A refusal is not a failure: the command never ran, so sending the reader to
  // debug the tool is the wrong instruction. The state, the badge shape and the
  // accessible name all have to say policy.
  it("renders a policy denial as its own state, not a failure", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t3",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
      denial: {
        capability: "shell",
        resource: "rm -rf /",
        scope: "workspace",
        source: "/workspace/.kiro/permissions.yaml",
        rule: { capability: "shell", effect: "deny", match: ["rm *"] },
      },
    });
    expect(card.dataset["outcome"]).toBe("denied");
    expect(card.dataset["denied"]).toBe("1");
    expect(card.querySelector(".tool-icon")?.classList.contains("is-denied")).toBe(true);
    expect(card.getAttribute("aria-label")).toContain("blocked by security policy");
  });

  // The rule and its file are the actionable half: the user owns the policy, so a
  // refusal that names what fired is one step from changing it.
  it("shows the matched rule and where it lives", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
      denial: {
        capability: "shell",
        resource: "curl evil.example",
        scope: "user",
        source: "/root/.kiro/permissions.yaml",
        rule: {
          capability: "shell",
          effect: "deny",
          match: ["curl *"],
          exclude: ["curl localhost*"],
        },
      },
    });
    // Behind the disclosure, and built on first open with the rest of the body:
    // the claim line already says "blocked by security policy" (the case above),
    // and the rule that fired is depth 2.
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    const text = card.querySelector(".tool-denial")?.textContent ?? "";
    card.remove();
    expect(text).toContain("shell");
    expect(text).toContain("curl *");
    expect(text).toContain("!curl localhost*");
    expect(text).toContain("/root/.kiro/permissions.yaml");
  });

  // An ordinary failure must keep reading as a failure; the denial state is
  // additive, not a reclassification of every red card.
  it("leaves an ordinary failure alone", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t5",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
    });
    expect(card.dataset["outcome"]).toBe("fail");
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelector(".tool-denial")).toBeNull();
    card.remove();
  });
});

// ---------------------------------------------------------------------------
// The header IS the disclosure's hit target (disclosure-row.ts).
//
// Before this, the chevron alone toggled a tool card while the group header it
// sits inside took a click anywhere — the same gesture, two answers. These pin
// the header surface, the two things inside it that must keep their own click,
// and the claim-only card that must stay inert.
// ---------------------------------------------------------------------------

describe("tool card: whole-header disclosure", () => {
  it("a click on the header toggles the card's details", () => {
    const card = buildToolCard({
      id: "hdr1",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const header = card.querySelector<HTMLElement>(".tool-header")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    header.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    header.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    card.remove();
  });

  it("a click on the title inside the summary toggles it too", () => {
    const card = buildToolCard({
      id: "hdr2",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const title = card.querySelector<HTMLElement>(".tool-title")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;

    title.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("a click on the description below the title toggles the card", () => {
    // Search/fetch/generic cards put their first input fact under the title.
    // The whole visible summary is one disclosure target, so that line cannot
    // be a dead strip inside a box that otherwise reads as a button.
    const card = buildToolCard({
      id: "hdr-subtitle",
      title: "remote_web_search",
      kind: "fetch",
      status: "completed",
      input: { query: "vibekit fold animation" },
      live: false,
    });
    document.body.appendChild(card);

    const subtitle = card.querySelector<HTMLElement>(".tool-subtitle")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(subtitle.textContent).toBe("vibekit fold animation");

    subtitle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("shows non-MCP tool names without underscores", () => {
    const card = buildToolCard({
      id: "hdr-human-title",
      title: "remote_web_search",
      kind: "fetch",
      status: "completed",
      input: { query: "vibekit" },
      live: false,
    });
    const title = card.querySelector<HTMLElement>(".tool-title")!;
    expect(title.textContent).toBe("remote web search");
    expect(card.dataset["title"]).toBe("remote web search");
  });

  it("the chevron still toggles exactly once", () => {
    // The forwarded click bubbles back into the header's listener, so a chevron
    // click could have counted twice and landed back where it started.
    const card = buildToolCard({
      id: "hdr3",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("the filename link opens the change and does NOT toggle the card", () => {
    const card = buildToolCard({
      id: "hdr4",
      title: "fsAppend",
      kind: "write",
      status: "completed",
      input: { path: "src/main.ts" },
      live: false,
    });
    document.body.appendChild(card);

    const link = card.querySelector<HTMLElement>(".tool-file-link")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(link).not.toBeNull();

    const before = opened.length;
    link.click();
    expect(opened.length).toBe(before + 1);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    card.remove();
  });

  it("a claim-only card has no toggle and its header stays inert", () => {
    // `readFile` resolves to kind `read`, whose depth 1 is "none": no toggle and
    // no details region, so the header must not become a control that opens an
    // empty one.
    const card = buildToolCard({
      id: "hdr5",
      title: "readFile",
      kind: "read",
      status: "completed",
      input: { path: "src/main.ts" },
      live: false,
    });
    document.body.appendChild(card);

    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(card.querySelector(".tool-details")).toBeNull();

    header.click();
    expect(card.querySelector("[aria-expanded]")).toBeNull();

    card.remove();
  });
});

// ---------------------------------------------------------------------------
// The card's BODY is built on first open.
//
// A collapsed card is a claim line, and a transcript mounts dozens of them, so
// nothing with a cost in it — the denial rows, the input dump, painting the
// output through the ANSI renderer and the path linkifier — runs until a reader
// asks. `.tool-output` is the exception: it is part of the shell, because the
// live update path streams chunks straight into it and a streaming card has
// usually not been opened.
// ---------------------------------------------------------------------------

describe("the details body is deferred to first open", () => {
  it("mounts an EMPTY details region with the output slot in it", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "hello from the build\n",
    });
    const details = card.querySelector(".tool-details");
    expect(details).not.toBeNull();
    // The slot exists so a streamed chunk has somewhere to land...
    expect(details?.querySelector(".tool-output")).not.toBeNull();
    // ...and nothing has been painted into it.
    expect(details?.querySelector("pre")).toBeNull();
  });

  it("paints the output when the disclosure is activated", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "hello from the build\n",
    });
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelector(".tool-output pre")?.textContent).toContain("hello from the build");
    card.remove();
  });

  it("builds once, however many times the disclosure is toggled", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "one line\n",
    });
    document.body.appendChild(card);
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    toggle?.click();
    toggle?.click();
    toggle?.click();
    expect(card.querySelectorAll(".tool-output pre")).toHaveLength(1);
    card.remove();
  });

  it("makes the output readable straight after expandToolDetails, which the failure path needs", async () => {
    // `messages-tools.ts` force-opens a failed card and then reads
    // `.tool-output`'s text to offer "Explain this error". So the body has to be
    // built by the time that call returns.
    const { expandToolDetails } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "failed",
      live: false,
      output: "cc: fatal error\n",
    });
    document.body.appendChild(card);
    expandToolDetails(card);
    expect(card.querySelector(".tool-output")?.textContent).toContain("cc: fatal error");
    card.remove();
  });
});
