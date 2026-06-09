// ---------------------------------------------------------------------------
// Code-block decoration: syntax highlight each `<pre><code>` block, wrap
// it with a Copy button, and for short shell snippets also render a Run
// button that dispatches to the shell via a callback (avoids a static
// cycle with shell.ts).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { highlightByLang, normalizeLang } from "./highlight.js";
import { ICON_COPY, ICON_PLAY, iconEl } from "./icons.js";

const SHELL_LANGS = new Set(["", "sh", "bash", "zsh", "shell", "console", "terminal"]);

type ShellRunCb = (cmd: string) => void;
let shellRunCb: ShellRunCb | null = null;

/** Wire the shell panel's run handler. Called once at startup by the
 *  shell module. */
export function setShellRunCallback(cb: ShellRunCb): void {
  shellRunCb = cb;
}

type CopyCb = (text: string) => void;
let copyCb: CopyCb | null = null;

/** Wire the clipboard copy handler. Called once at startup alongside
 *  setShellRunCallback to avoid a dynamic import cycle. */
export function setCopyCallback(cb: CopyCb): void {
  copyCb = cb;
}

export function decorateCodeBlocks(root: HTMLElement): void {
  for (const pre of root.querySelectorAll("pre")) {
    if (pre.parentElement?.classList.contains("code-wrap")) {
      continue;
    }
    if (pre.querySelector(".code-actions") !== null) {
      continue;
    }
    decorateOne(pre);
  }
}

function decorateOne(pre: HTMLElement): void {
  const codeEl = pre.querySelector("code");
  const lang = extractLang(pre, codeEl);
  const text = (codeEl ?? pre).textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition

  // Highlight in place. Unknown languages pass through as plain escaped
  // text (renderMarkdown already escaped it; we only swap innerHTML if
  // we can highlight).
  const hlLang = normalizeLang(lang);
  if (codeEl !== null && hlLang !== "") {
    codeEl.innerHTML = highlightByLang(text, hlLang);
  }

  const wrap = el("div", { className: "code-wrap" });
  pre.parentElement?.insertBefore(wrap, pre);
  wrap.appendChild(pre);

  const actions = el("div", { className: "code-actions" }, makeCopyButton(text));
  if (isRunnableShell(lang, text)) {
    actions.appendChild(makeRunButton(text));
  }
  wrap.appendChild(actions);
}

function extractLang(pre: HTMLElement, code: HTMLElement | null): string {
  const preMatch = /(?:^|\s)code\s+(\S+)/.exec(pre.className);
  if (preMatch?.[1] !== undefined && preMatch[1] !== "") {
    return preMatch[1].toLowerCase();
  }
  if (code !== null) {
    const codeMatch = /language-(\S+)/.exec(code.className);
    if (codeMatch?.[1] !== undefined) {
      return codeMatch[1].toLowerCase();
    }
  }
  return "";
}

export function isRunnableShell(lang: string, text: string): boolean {
  if (!SHELL_LANGS.has(lang)) {
    return false;
  }
  const trimmed = text.trim();
  if (trimmed === "") {
    return false;
  }
  if (trimmed.startsWith("#!")) {
    return false;
  }
  const lineCount = trimmed.split("\n").filter((l) => l.trim() !== "").length;
  if (lineCount > 3) {
    return false;
  }
  if (/\b(sudo|ssh|scp|rsync)\b/.test(trimmed)) {
    return false;
  }
  return true;
}

function makeCopyButton(text: string): HTMLButtonElement {
  const btn = el(
    "button",
    { className: "code-act-btn", "data-tooltip": "Copy", "aria-label": "Copy" },
    iconEl(ICON_COPY),
  ) as HTMLButtonElement;
  let timer: ReturnType<typeof setTimeout> | undefined;
  btn.addEventListener("click", () => {
    if (copyCb !== null) {
      copyCb(text);
      btn.textContent = "✓";
      clearTimeout(timer);
      timer = setTimeout(() => {
        btn.replaceChildren(iconEl(ICON_COPY));
      }, 1500);
    }
  });
  return btn;
}

function makeRunButton(text: string): HTMLButtonElement {
  const btn = el("button", { className: "code-act-btn" }, iconEl(ICON_PLAY)) as HTMLButtonElement;
  if (shellRunCb === null) {
    btn.setAttribute("data-tooltip", "Shell not available");
    btn.setAttribute("aria-label", "Shell not available");
    btn.disabled = true;
  } else {
    btn.setAttribute("data-tooltip", "Run in shell");
    btn.setAttribute("aria-label", "Run in shell");
    btn.addEventListener("click", () => {
      shellRunCb?.(text.trim());
    });
  }
  return btn;
}
