// Unit tests for code-blocks.ts — isRunnableShell pure predicate.
import { describe, it, expect } from "vitest";
import { isRunnableShell } from "./code-blocks.js";

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
