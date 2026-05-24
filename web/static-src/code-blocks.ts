// ---------------------------------------------------------------------------
// Code-block decoration: syntax highlight each `<pre><code>` block, wrap
// it with a Copy button, and for short shell snippets also render a Run
// button that dispatches to the shell via a callback (avoids a static
// cycle with shell.ts).
// ---------------------------------------------------------------------------

import { highlightByLang, normalizeLang } from "./highlight.js";
import { ICON_COPY, ICON_PLAY, iconEl } from "./icons.js";

const SHELL_LANGS = new Set(["", "sh", "bash", "zsh", "shell", "console", "terminal"]);

type ShellRunCb = (cmd: string) => void;
let shellRunCb: ShellRunCb | null = null;

/** Wire the shell panel's run handler. Called once at startup by the
 *  shell module. */
export function setShellRunCallback(cb: ShellRunCb): void { shellRunCb = cb; }

export function decorateCodeBlocks(root: HTMLElement): void {
  for (const pre of root.querySelectorAll("pre")) {
    if (pre.parentElement?.classList.contains("code-wrap")) continue;
    if (pre.querySelector(".code-actions") !== null) continue;
    decorateOne(pre);
  }
}

function decorateOne(pre: HTMLElement): void {
  const codeEl = pre.querySelector("code");
  const lang = extractLang(pre, codeEl);
  const text = (codeEl ?? pre).textContent ?? "";

  // Highlight in place. Unknown languages pass through as plain escaped
  // text (renderMarkdown already escaped it; we only swap innerHTML if
  // we can highlight).
  const hlLang = normalizeLang(lang);
  if (codeEl !== null && hlLang !== "") {
    codeEl.innerHTML = highlightByLang(text, hlLang);
  }

  const wrap = document.createElement("div");
  wrap.className = "code-wrap";
  pre.parentElement?.insertBefore(wrap, pre);
  wrap.appendChild(pre);

  const actions = document.createElement("div");
  actions.className = "code-actions";
  actions.appendChild(makeCopyButton(text));
  if (isRunnableShell(lang, text)) actions.appendChild(makeRunButton(text));
  wrap.appendChild(actions);
}

function extractLang(pre: HTMLElement, code: HTMLElement | null): string {
  const preMatch = /(?:^|\s)code\s+(\S+)/.exec(pre.className);
  if (preMatch?.[1] !== undefined && preMatch[1] !== "") return preMatch[1].toLowerCase();
  if (code !== null) {
    const codeMatch = /language-(\S+)/.exec(code.className);
    if (codeMatch?.[1] !== undefined) return codeMatch[1].toLowerCase();
  }
  return "";
}

export function isRunnableShell(lang: string, text: string): boolean {
  if (!SHELL_LANGS.has(lang)) return false;
  const trimmed = text.trim();
  if (trimmed === "") return false;
  if (trimmed.startsWith("#!")) return false;
  const lineCount = trimmed.split("\n").filter((l) => l.trim() !== "").length;
  if (lineCount > 3) return false;
  if (/\b(sudo|ssh|scp|rsync)\b/.test(trimmed)) return false;
  return true;
}

function makeCopyButton(text: string): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.className = "code-act-btn";
  btn.setAttribute("data-tooltip", "Copy");
  btn.replaceChildren(iconEl(ICON_COPY));
  btn.addEventListener("click", () => {
    void import("./actions/messages.js").then(({ copyClipboard }) =>
      copyClipboard.dispatch(text, { silent: true }).then((r) => {
        if (r === null) return;
        btn.textContent = "✓";
        setTimeout(() => { btn.replaceChildren(iconEl(ICON_COPY)); }, 1500);
      }),
    );
  });
  return btn;
}

function makeRunButton(text: string): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.className = "code-act-btn";
  btn.setAttribute("data-tooltip", "Run in shell");
  btn.replaceChildren(iconEl(ICON_PLAY));
  btn.addEventListener("click", () => { shellRunCb?.(text.trim()); });
  return btn;
}
