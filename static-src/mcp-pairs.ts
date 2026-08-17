// ---------------------------------------------------------------------------
// MCP key/value pair editor — reusable sub-module for env vars and headers.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { type KeyPair, SECRET_MASK } from "./mcp-state.js";
import { ICON_CLOSE } from "./icons.js";

export type PairKind = "env" | "header";

/** What the publisher declared about one field: why it exists, whether the
 *  server needs a value, whether that value is a credential.
 *
 *  It arrives with a registry hit (`environmentVariables` / `headers` on the
 *  upstream record) and it is the fix for the most common MCP setup failure: a
 *  server that installs cleanly, does nothing, and sends the user off to read
 *  the publisher's docs to find out it needed a token. Disclosure only — an
 *  unfilled required field is still saveable, because the value may legitimately
 *  come later and a gate here would be a nanny. */
interface DeclaredField {
  description?: string | undefined;
  required?: boolean | undefined;
  secret?: boolean | undefined;
}

/** A row of the editor: the pair itself plus, when a registry hit supplied one,
 *  what the publisher said about it. */
export type EditablePair = KeyPair & { declared?: DeclaredField | undefined };

export function renderKeyPairList(
  host: HTMLDivElement,
  pairs: EditablePair[],
  kind: PairKind,
): void {
  host.replaceChildren();
  if (pairs.length === 0) {
    appendKeyPair(host, { name: "", value: "" }, kind);
    return;
  }
  for (const kv of pairs) {
    appendKeyPair(host, kv, kind);
  }
}

// Tracks secret inputs the user actively typed into (via input event).
const touchedInputs = new WeakSet<HTMLInputElement>();

// Per-row id source for the aria-describedby link between a value input and its
// declared-field line. Module-scoped so two lists in one modal can't collide.
let pairMetaSeq = 0;

export function appendKeyPair(host: HTMLDivElement, kv: EditablePair, kind: PairKind): void {
  const row = el("div", { className: "mcp-pair-row" });

  const nameIn = el("input", {
    type: "text",
    className: "tool-form-input mcp-pair-name",
    placeholder: kind === "env" ? "VAR_NAME" : "Header-Name",
    value: kv.name,
  });

  const valIn = el("input", {
    type: kv.value === SECRET_MASK ? "password" : "text",
    className: "tool-form-input mcp-pair-value",
    placeholder: placeholderFor(kind, kv.declared),
    value: kv.value,
  }) as HTMLInputElement;
  if (kv.value === SECRET_MASK) {
    valIn.dataset["secret"] = "true";
    valIn.dataset["wasSecret"] = "true";
  }

  valIn.addEventListener("input", () => {
    touchedInputs.add(valIn);
  });

  valIn.addEventListener("focus", () => {
    if (valIn.dataset["secret"] === "true" && valIn.value === SECRET_MASK) {
      valIn.type = "text";
      valIn.value = "";
      delete valIn.dataset["secret"];
    }
  });

  valIn.addEventListener("blur", () => {
    if (valIn.value === "" && valIn.dataset["wasSecret"] === "true" && !touchedInputs.has(valIn)) {
      valIn.value = SECRET_MASK;
      valIn.type = "password";
      valIn.dataset["secret"] = "true";
    }
    touchedInputs.delete(valIn);
  });

  const del = el("button", {
    type: "button",
    className: "icon-btn mcp-pair-del",
    title: "Remove",
    "aria-label": "Remove",
  });
  del.innerHTML = ICON_CLOSE;
  del.addEventListener("click", () => {
    row.remove();
  });

  row.append(nameIn, valIn, del);
  const meta = buildDeclaredLine(kv.declared);
  if (meta !== null) {
    meta.id = `mcp-pair-meta-${++pairMetaSeq}`;
    valIn.setAttribute("aria-describedby", meta.id);
    if (kv.declared?.required === true) {
      valIn.setAttribute("aria-required", "true");
    }
    row.appendChild(meta);
  }
  host.appendChild(row);
}

/** The publisher's own words about a field, with its required / secret markers.
 *  Returns null when the registry told us nothing, so a hand-typed row keeps
 *  exactly the shape it had. */
function buildDeclaredLine(declared: DeclaredField | undefined): HTMLElement | null {
  if (declared === undefined) {
    return null;
  }
  const marks: HTMLElement[] = [];
  if (declared.required === true) {
    marks.push(el("span", { className: "mcp-pair-mark mcp-pair-mark-required" }, "Required"));
  }
  if (declared.secret === true) {
    marks.push(el("span", { className: "mcp-pair-mark" }, "Secret"));
  }
  const text = (declared.description ?? "").trim();
  if (marks.length === 0 && text === "") {
    return null;
  }
  const line = el("p", { className: "mcp-pair-meta" }, ...marks) as HTMLParagraphElement;
  if (text !== "") {
    line.appendChild(el("span", { className: "mcp-pair-desc" }, text));
  }
  return line;
}

/** Placeholder text for a value input. A declared secret says so, because the
 *  field otherwise looks like any other and the user has no cue that what they
 *  paste is stored masked and never read back. */
function placeholderFor(kind: PairKind, declared: DeclaredField | undefined): string {
  if (declared?.secret === true) {
    return kind === "env" ? "token (stored masked)" : "value (stored masked)";
  }
  return "value";
}

export function collectKeyPairs(host: HTMLDivElement): KeyPair[] {
  const out: KeyPair[] = [];
  for (const row of host.querySelectorAll<HTMLDivElement>(".mcp-pair-row")) {
    const nameIn = row.querySelector<HTMLInputElement>(".mcp-pair-name");
    const valIn = row.querySelector<HTMLInputElement>(".mcp-pair-value");
    if (nameIn === null || valIn === null) {
      continue;
    }
    const n = nameIn.value.trim();
    if (n === "") {
      continue;
    }
    const v = valIn.dataset["secret"] === "true" ? SECRET_MASK : valIn.value;
    out.push({ name: n, value: v });
  }
  return out;
}
