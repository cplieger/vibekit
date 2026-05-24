// ---------------------------------------------------------------------------
// MCP key/value pair editor — reusable sub-module for env vars and headers.
// ---------------------------------------------------------------------------

import { type KeyPair, SECRET_MASK } from "./mcp-state.js";
import { ICON_CLOSE } from "./icons.js";

export type PairKind = "env" | "header";

export function renderKeyPairList(host: HTMLDivElement, pairs: KeyPair[], kind: PairKind): void {
  host.replaceChildren();
  if (pairs.length === 0) {
    appendKeyPair(host, { name: "", value: "" }, kind);
    return;
  }
  for (const kv of pairs) appendKeyPair(host, kv, kind);
}

// Tracks secret inputs the user actively typed into (via input event).
const touchedInputs = new WeakSet<HTMLInputElement>();

export function appendKeyPair(host: HTMLDivElement, kv: KeyPair, kind: PairKind): void {
  const row = document.createElement("div");
  row.className = "mcp-pair-row";

  const nameIn = document.createElement("input");
  nameIn.type = "text";
  nameIn.className = "tool-form-input mcp-pair-name";
  nameIn.placeholder = kind === "env" ? "VAR_NAME" : "Header-Name";
  nameIn.value = kv.name;

  const valIn = document.createElement("input");
  valIn.type = kv.value === SECRET_MASK ? "password" : "text";
  valIn.className = "tool-form-input mcp-pair-value";
  valIn.placeholder = kind === "env" ? "value" : "value";
  valIn.value = kv.value;
  if (kv.value === SECRET_MASK) {
    valIn.dataset["secret"] = "true";
    valIn.dataset["wasSecret"] = "true";
  }

  valIn.addEventListener("input", () => { touchedInputs.add(valIn); });

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

  const del = document.createElement("button");
  del.type = "button";
  del.className = "icon-btn mcp-pair-del";
  del.title = "Remove";
  del.setAttribute("aria-label", "Remove");
  del.innerHTML = ICON_CLOSE;
  del.addEventListener("click", () => row.remove());

  row.append(nameIn, valIn, del);
  host.appendChild(row);
}

export function collectKeyPairs(host: HTMLDivElement): KeyPair[] {
  const out: KeyPair[] = [];
  for (const row of host.querySelectorAll<HTMLDivElement>(".mcp-pair-row")) {
    const nameIn = row.querySelector<HTMLInputElement>(".mcp-pair-name");
    const valIn = row.querySelector<HTMLInputElement>(".mcp-pair-value");
    if (nameIn === null || valIn === null) continue;
    const n = nameIn.value.trim();
    if (n === "") continue;
    const v = valIn.dataset["secret"] === "true" ? SECRET_MASK : valIn.value;
    out.push({ name: n, value: v });
  }
  return out;
}
