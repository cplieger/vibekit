// @vitest-environment happy-dom
// Unit tests for code-blocks.ts: the isRunnableShell predicate, the language
// label the title bar prints, and the two-state decoration (a fence still
// streaming vs. one that closed).
import { describe, it, expect, vi } from "vitest";
import {
  isRunnableShell,
  extractLang,
  decorateCodeBlocks,
  decorateStreamingCodeTail,
  setCopyCallback,
  setShellRunCallback,
} from "./code-blocks.js";

describe("isRunnableShell", () => {
  const cases: { name: string; lang: string; text: string; expected: boolean }[] = [
    { name: "valid single-line shell command", lang: "bash", text: "echo hello", expected: true },
    { name: "empty lang treated as shell", lang: "", text: "ls -la", expected: true },
    { name: "sh lang accepted", lang: "sh", text: "pwd", expected: true },
    { name: "zsh lang accepted", lang: "zsh", text: "cd /tmp", expected: true },
    { name: "console lang accepted", lang: "console", text: "whoami", expected: true },
    { name: "terminal lang accepted", lang: "terminal", text: "date", expected: true },
    { name: "shell lang accepted", lang: "shell", text: "uname -a", expected: true },
    { name: "non-shell lang rejected", lang: "python", text: "print('hi')", expected: false },
    { name: "non-shell lang go rejected", lang: "go", text: "fmt.Println()", expected: false },
    { name: "empty text rejected", lang: "bash", text: "", expected: false },
    { name: "whitespace-only text rejected", lang: "bash", text: "   \n  ", expected: false },
    {
      name: "shebang prefix rejected",
      lang: "bash",
      text: "#!/bin/bash\necho hi",
      expected: false,
    },
    {
      name: "shebang with env rejected",
      lang: "sh",
      text: "#!/usr/bin/env bash\nls",
      expected: false,
    },
    { name: "4-line script rejected", lang: "bash", text: "a\nb\nc\nd", expected: false },
    { name: "3-line script accepted", lang: "bash", text: "a\nb\nc", expected: true },
    { name: "sudo command rejected", lang: "bash", text: "sudo apt install foo", expected: false },
    { name: "ssh command rejected", lang: "bash", text: "ssh user@host", expected: false },
    {
      name: "scp command rejected",
      lang: "bash",
      text: "scp file.txt host:/tmp/",
      expected: false,
    },
    { name: "rsync command rejected", lang: "bash", text: "rsync -av src/ dest/", expected: false },
    {
      name: "dangerous command mid-line rejected",
      lang: "sh",
      text: "echo hi && sudo rm -rf /",
      expected: false,
    },
    {
      name: "text with blank lines counts only non-empty",
      lang: "bash",
      text: "a\n\nb\n\nc",
      expected: true,
    },
    {
      name: "3 non-empty lines with trailing newline accepted",
      lang: "bash",
      text: "a\nb\nc\n",
      expected: true,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(isRunnableShell(tc.lang, tc.text)).toBe(tc.expected);
    });
  }
});

describe("extractLang", () => {
  // Two channels, and only the second is live on the markdown path: the
  // renderer sets a bare `code` class on the <pre> and `language-<tag>` on the
  // <code>. The <pre> channel is kept for callers that pass a lang there.
  const cases: { name: string; pre: string; code: string | null; want: string }[] = [
    { name: "the code element's language- class", pre: "code", code: "language-go", want: "go" },
    { name: "the pre element's own class", pre: "code python", code: null, want: "python" },
    {
      name: "the pre channel wins over the code channel",
      pre: "code rust",
      code: "language-go",
      want: "rust",
    },
    { name: "no language anywhere", pre: "code", code: null, want: "" },
    { name: "no classes at all", pre: "", code: null, want: "" },
    { name: "the tag is lowercased", pre: "code", code: "language-TypeScript", want: "typescript" },
    {
      name: "an unknown tag passes through",
      pre: "code",
      code: "language-brainfuck",
      want: "brainfuck",
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const pre = document.createElement("pre");
      pre.className = tc.pre;
      let code: HTMLElement | null = null;
      if (tc.code !== null) {
        code = document.createElement("code");
        code.className = tc.code;
        pre.appendChild(code);
      }
      expect(extractLang(pre, code)).toBe(tc.want);
    });
  }
});

/** A rendered code block as smd-renderer builds it. */
function fixture(lang: string, text: string): { root: HTMLElement; pre: HTMLElement } {
  const root = document.createElement("div");
  const pre = document.createElement("pre");
  pre.className = "code";
  const code = document.createElement("code");
  if (lang !== "") {
    code.className = `language-${lang}`;
  }
  code.textContent = text;
  pre.appendChild(code);
  root.appendChild(pre);
  return { root, pre };
}

function head(root: HTMLElement): HTMLElement | null {
  return root.querySelector(".code-wrap > .code-head");
}

describe("decorateCodeBlocks: the title bar", () => {
  it("prints the raw fence tag as the language label", () => {
    const { root } = fixture("go", "func main() {}");
    decorateCodeBlocks(root);
    expect(head(root)?.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("prints an unrecognised fence tag verbatim rather than dropping it", () => {
    // The label is the author's tag; highlight.ts's normalizeLang decides
    // HIGHLIGHTING and has no display-name map to consult.
    const { root } = fixture("brainfuck", "++++");
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-lang")?.textContent).toBe("brainfuck");
  });

  it("leaves the label empty for a fence with no tag, and still builds the bar", () => {
    const { root } = fixture("", "ls -la");
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-lang")?.textContent).toBe("");
    expect(head(root)).not.toBeNull();
  });

  it("puts the actions inside the title bar, not floating over the pre", () => {
    const { root } = fixture("go", "func main() {}");
    decorateCodeBlocks(root);
    expect(head(root)?.querySelector(".code-actions")).not.toBeNull();
    expect(root.querySelector(".code-wrap > .code-actions")).toBeNull();
  });

  it("puts the bar before the pre inside the wrapper", () => {
    const { root } = fixture("go", "x");
    decorateCodeBlocks(root);
    const kids = [...(root.querySelector(".code-wrap")?.children ?? [])].map((e) =>
      e.tagName.toLowerCase(),
    );
    expect(kids).toEqual(["div", "pre"]);
  });

  it("is idempotent: a second pass adds no second bar and no second button", () => {
    const { root } = fixture("bash", "echo hi");
    decorateCodeBlocks(root);
    decorateCodeBlocks(root);
    decorateCodeBlocks(root);
    expect(root.querySelectorAll(".code-head")).toHaveLength(1);
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2); // copy + run
  });

  it("highlights a known language and leaves an unknown one as text", () => {
    const { root: known } = fixture("go", "func main() {}");
    decorateCodeBlocks(known);
    expect(known.querySelector("code")?.querySelector(".hl-kw, [class^='hl-']")).not.toBeNull();

    const { root: unknown } = fixture("brainfuck", "++++");
    decorateCodeBlocks(unknown);
    expect(unknown.querySelector("code")?.children).toHaveLength(0);
  });

  it("offers Run only for a runnable shell snippet", () => {
    const { root: shell } = fixture("bash", "echo hi");
    decorateCodeBlocks(shell);
    expect(shell.querySelectorAll(".code-act-btn")).toHaveLength(2);

    const { root: go } = fixture("go", "func main() {}");
    decorateCodeBlocks(go);
    expect(go.querySelectorAll(".code-act-btn")).toHaveLength(1);
  });
});

describe("decorateStreamingCodeTail: a fence that has not closed", () => {
  it("gives the open block its bar and its Copy button", () => {
    // The renderer's per-block callback only fires on CLOSE, so without this
    // sweep a streaming block has no language, no Copy and no wrapper at all.
    const { root } = fixture("go", "func main() {");
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("streaming");
    expect(root.querySelector(".code-lang")?.textContent).toBe("go");
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(1);
  });

  it("withholds highlighting and Run while the text is still growing", () => {
    const { root } = fixture("bash", "echo hi");
    decorateStreamingCodeTail(root);
    // No Run: an incomplete command is the one that must not reach a shell.
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(1);
    expect(root.querySelector("code")?.children).toHaveLength(0);
  });

  it("upgrades in place when the fence closes: highlight, Run, one bar", () => {
    const { root } = fixture("bash", "echo hi");
    decorateStreamingCodeTail(root);
    decorateCodeBlocks(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(root.querySelectorAll(".code-head")).toHaveLength(1);
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2);
  });

  it("leaves a block that already closed alone", () => {
    // The close callback runs during the parse slice, so the tail sweep right
    // after it sees the just-finalized block as "last".
    const { root } = fixture("bash", "echo hi");
    decorateCodeBlocks(root);
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(root.querySelectorAll(".code-act-btn")).toHaveLength(2);
  });

  it("copies the text as it is at CLICK time, not as it was when the button was built", () => {
    const copied: string[] = [];
    setCopyCallback((t) => copied.push(t));
    const { root } = fixture("go", "func main() {");
    decorateStreamingCodeTail(root);
    // More of the block arrives, exactly as the parser appends it.
    const code = root.querySelector("code");
    code?.appendChild(document.createTextNode("\n\tprintln()\n}"));
    root.querySelector<HTMLButtonElement>(".code-act-btn")?.click();
    expect(copied).toEqual(["func main() {\n\tprintln()\n}"]);
  });

  it("does nothing when there is no code block", () => {
    const root = document.createElement("div");
    root.appendChild(document.createElement("p"));
    decorateStreamingCodeTail(root);
    expect(root.querySelector(".code-wrap")).toBeNull();
  });

  it("wires Run to the shell callback with the trimmed command", () => {
    const run = vi.fn();
    setShellRunCallback(run);
    const { root } = fixture("bash", "  echo hi\n");
    decorateCodeBlocks(root);
    const buttons = [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")];
    buttons[1]?.click();
    expect(run).toHaveBeenCalledWith("echo hi");
  });
});
