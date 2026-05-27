// ---------------------------------------------------------------------------
// Inline file-path linkification. Scans text nodes inside rendered bubbles
// and converts `src/foo.ts:42` mentions into clickable buttons that open
// the editor at the target line. Skips <code>, <pre>, <a>, <button> so
// code samples aren't clobbered.
// ---------------------------------------------------------------------------

import { openFile } from "./editor-openers.js";
import { fileIcon } from "./icons.js";
import { FILE_EXTS } from "./file-extensions.js";

const PATH_PATTERN =
  "(?<![\\w/.-])([\\w.-]+\\/[\\w./-]*\\.(?:" +
  FILE_EXTS.join("|") +
  "))(?::(\\d+)(?::\\d+)?)?(?![\\w/.-])";

// Non-global version for acceptNode test (no lastIndex mutation).
const PATH_TEST_RX = new RegExp(PATH_PATTERN);
// Global version for replacePaths exec loop.
const PATH_EXEC_RX = new RegExp(PATH_PATTERN, "g");

const SKIP_TAGS = new Set(["CODE", "PRE", "A", "BUTTON"]);

export function linkifyPaths(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      const parent = n.parentElement;
      if (parent === null) {
        return NodeFilter.FILTER_REJECT;
      }
      if (SKIP_TAGS.has(parent.tagName)) {
        return NodeFilter.FILTER_REJECT;
      }
      return PATH_TEST_RX.test(n.nodeValue ?? "")
        ? NodeFilter.FILTER_ACCEPT
        : NodeFilter.FILTER_REJECT;
    },
  });
  const targets: Text[] = [];
  let cur: Node | null;
  while ((cur = walker.nextNode()) !== null) {
    targets.push(cur as Text);
  }
  for (const textNode of targets) {
    replacePaths(textNode);
  }
}

function replacePaths(textNode: Text): void {
  const text = textNode.nodeValue ?? "";
  PATH_EXEC_RX.lastIndex = 0;
  const frag = document.createDocumentFragment();
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = PATH_EXEC_RX.exec(text)) !== null) {
    if (m.index > last) {
      frag.appendChild(document.createTextNode(text.slice(last, m.index)));
    }
    frag.appendChild(makeLink(m[1]!, m[2]));
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    frag.appendChild(document.createTextNode(text.slice(last)));
  }
  textNode.replaceWith(frag);
}

function makeLink(path: string, lineStr: string | undefined): HTMLButtonElement {
  const line = lineStr !== undefined ? parseInt(lineStr, 10) : undefined;
  const basename = path.split("/").pop() ?? path;
  const label = line !== undefined ? `${basename}:${String(line)}` : basename;
  const btn = document.createElement("button");
  btn.className = "inline-file-link";
  btn.title = line !== undefined ? `${path}:${String(line)}` : path;
  const iconSpan = document.createElement("span");
  iconSpan.className = "inline-file-icon";
  iconSpan.innerHTML = fileIcon(basename, false);
  const labelSpan = document.createElement("span");
  labelSpan.textContent = label;
  btn.append(iconSpan, labelSpan);
  btn.addEventListener("click", () => {
    openFile(path, line);
  });
  return btn;
}
