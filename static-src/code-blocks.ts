// ---------------------------------------------------------------------------
// Code-block decoration: a title bar carrying the language and the block's
// actions, syntax highlighting, and a Run button for short shell snippets that
// dispatches to the shell via a callback (avoids a static cycle with shell.ts).
//
// TWO STATES, ONE PATH. A fenced block is decorated PROVISIONALLY while it is
// still streaming — wrapper, language label, Copy — and FINALLY once the fence
// closes, which is when the text stops growing and highlighting becomes
// meaningful. Both go through `decorateBlock`, which is idempotent and upgrades
// in place, because the alternative (a separate streaming builder) is two
// definitions of one piece of chrome.
//
// The provisional pass exists because the renderer's per-block callback only
// fires when a block CLOSES. An unterminated fence at the tail of a live turn
// therefore had no highlight, no language and no Copy button — and, since
// `parser_end` does not close open tokens either, it kept none of them after the
// turn ended. So the sweep is not cosmetic: it is the only thing that decorates
// a block the model never closed.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { highlightByLang, normalizeLang } from "./highlight.js";
import { ICON_COPY, ICON_PLAY } from "./icons.js";
import { iconEl } from "./icon-el.js";

const SHELL_LANGS = new Set(["", "sh", "bash", "zsh", "shell", "console", "terminal"]);

/** Decoration state, held on the wrapper so a second pass knows what is left to
 *  do. `streaming` has chrome but no highlighting; `final` has both. */
const STATE_ATTR = "data-code-state";

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

/** Decorate every code block under `root` as FINAL. Called from the renderer's
 *  per-block-complete hook, and again when a markdown stream ends so a fence the
 *  model never closed still gets its highlighting and its buttons. */
export function decorateCodeBlocks(root: HTMLElement): void {
  for (const pre of root.querySelectorAll("pre")) {
    decorateBlock(pre, false);
  }
}

/** Decorate the block currently streaming in, if there is one.
 *
 *  The open block is the LAST `<pre>` under the host: the parser is append-only,
 *  so nothing can arrive after it while it is open. A `final` wrapper is left
 *  alone, which is what keeps this safe to call after every parse slice — the
 *  block that just closed is finished business. */
export function decorateStreamingCodeTail(root: HTMLElement): void {
  const pres = root.querySelectorAll("pre");
  const last = pres[pres.length - 1];
  if (last === undefined) {
    return;
  }
  decorateBlock(last, true);
}

/** Build or upgrade one block's decoration.
 *
 *  `provisional` means the text is still arriving: chrome yes, highlighting no
 *  (a highlight pass over a half-written statement is wrong more often than it
 *  is right), and no Run button (an incomplete command is exactly the one that
 *  must not be offered to a shell). */
function decorateBlock(pre: HTMLElement, provisional: boolean): void {
  const existing = pre.parentElement;
  const wrapped = existing?.classList.contains("code-wrap") === true ? existing : null;
  if (wrapped?.getAttribute(STATE_ATTR) === "final") {
    return;
  }
  const wrap = wrapped ?? wrapBlock(pre);
  if (provisional) {
    wrap.setAttribute(STATE_ATTR, "streaming");
    return;
  }
  finalizeBlock(wrap, pre);
}

/** Wrap a bare `<pre>` and give it its title bar: the language on the left, the
 *  actions on the right. Copy reads the text at CLICK time so the same button
 *  serves a streaming block and a finished one. */
function wrapBlock(pre: HTMLElement): HTMLElement {
  const wrap = el("div", { className: "code-wrap" });
  pre.parentElement?.insertBefore(wrap, pre);

  const actions = el(
    "div",
    { className: "code-actions" },
    makeCopyButton(() => blockText(pre)),
  );
  const head = el(
    "div",
    { className: "code-head" },
    el("span", { className: "code-lang" }, extractLang(pre, pre.querySelector("code"))),
    actions,
  );
  wrap.appendChild(head);
  wrap.appendChild(pre);
  return wrap;
}

/** Promote a block to its finished form: highlight in place, offer Run when the
 *  snippet qualifies. */
function finalizeBlock(wrap: HTMLElement, pre: HTMLElement): void {
  wrap.setAttribute(STATE_ATTR, "final");
  const codeEl = pre.querySelector("code");
  const lang = extractLang(pre, codeEl);
  const text = blockText(pre);

  const label = wrap.querySelector(":scope > .code-head > .code-lang");
  if (label !== null) {
    label.textContent = lang;
  }

  // Highlight in place. Unknown languages pass through as plain escaped
  // text (renderMarkdown already escaped it; we only swap innerHTML if
  // we can highlight).
  const hlLang = normalizeLang(lang);
  if (codeEl !== null && hlLang !== "") {
    codeEl.innerHTML = highlightByLang(text, hlLang);
  }

  const actions = wrap.querySelector(":scope > .code-head > .code-actions");
  if (actions !== null && isRunnableShell(lang, text)) {
    actions.appendChild(makeRunButton(text));
  }
}

/** The block's plain text, read live so a copy during streaming copies what is
 *  on screen rather than what was there when the button was built. */
function blockText(pre: HTMLElement): string {
  const codeEl = pre.querySelector("code");
  return (codeEl ?? pre).textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
}

/** The fence tag as written, lowercased. Two channels: the `<pre>`'s own class
 *  (dormant on the markdown path, which sets a bare `code`) and the `<code>`'s
 *  `language-*` class, which is what smd-renderer writes. */
export function extractLang(pre: HTMLElement, code: HTMLElement | null): string {
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

/** Whether a finished fence earns the button that types it into the shell.
 *
 *  SINGLE LINE ONLY, and the test is for a line terminator rather than a line
 *  COUNT. `handle.send` writes raw bytes to the PTY, so a newline anywhere in
 *  the payload runs the text before it the instant the PTY sees it — which is
 *  exactly what typing-not-running exists to prevent. A count cannot express
 *  that: it ignores blank lines, so "echo a\n\necho b" counts two and
 *  "echo a\n" counts one, while both carry a terminator that executes. `\r` is
 *  in the class on purpose: a lone CR is Enter to a PTY just as much as LF is,
 *  and a fence pasted from a CRLF source carries them.
 *
 *  A multi-line block is not refused, only demoted: Copy is on every block
 *  unconditionally (wrapBlock), so the user pastes it into the terminal
 *  themselves and the terminal's own bracketed-paste handling applies.
 *
 *  There is deliberately no command denylist. Nothing executes until a
 *  keystroke, so a word filter gates nothing that Enter does not already gate,
 *  and the four words it matched (sudo|ssh|scp|rsync) were never a boundary
 *  anyway: `env sudo`, `$(which sudo)`, `doas`, `pkexec` and a workspace shell
 *  function that runs ssh internally all passed it. */
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
  return !/[\r\n]/.test(trimmed);
}

function makeCopyButton(getText: () => string): HTMLButtonElement {
  const btn = el(
    "button",
    { className: "code-act-btn", "data-tooltip": "Copy", "aria-label": "Copy" },
    iconEl(ICON_COPY),
  ) as HTMLButtonElement;
  let timer: ReturnType<typeof setTimeout> | undefined;
  btn.addEventListener("click", () => {
    if (copyCb !== null) {
      copyCb(getText());
      btn.textContent = "✓";
      clearTimeout(timer);
      timer = setTimeout(() => {
        btn.replaceChildren(iconEl(ICON_COPY));
      }, 1500);
    }
  });
  return btn;
}

/** The button that puts the command at the shell prompt.
 *
 *  The copy says TYPE rather than RUN because that is what the click does: the
 *  command lands at the prompt with the cursor after it and waits for Enter,
 *  which is the confirmation. */
function makeRunButton(text: string): HTMLButtonElement {
  const btn = el("button", { className: "code-act-btn" }, iconEl(ICON_PLAY)) as HTMLButtonElement;
  if (shellRunCb === null) {
    btn.setAttribute("data-tooltip", "Shell not available");
    btn.setAttribute("aria-label", "Shell not available");
    btn.disabled = true;
  } else {
    btn.setAttribute("data-tooltip", "Type in shell");
    btn.setAttribute("aria-label", "Type in shell, without running it");
    btn.addEventListener("click", () => {
      shellRunCb?.(text.trim());
    });
  }
  return btn;
}
