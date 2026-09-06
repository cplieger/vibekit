// ---------------------------------------------------------------------------
// Character-reference decoding: the half that is ALWAYS in the initial bundle.
//
// Numeric references and the five XML predefined names decode here, with no
// import of the 2,125-name WHATWG table. That split is a security boundary: a
// link destination is decoded before `isSafeUrl` reads it, so
// `javascript&#58;alert(1)` must resolve to a scheme the gate can refuse even on
// a page where the lazy chunk never arrived.
//
// `entitiesReady()` fetches the table and installs it over `XML_ENTITIES`. Until
// it lands — and for the rest of the page's life if it never does — every other
// name is unresolved, which `decode_entity` already renders as the text that was
// typed.
// ---------------------------------------------------------------------------

/** The longest name in the WHATWG table, which bounds how long the parser holds
 *  a candidate reference before deciding it is not one.
 *
 *  Declared here rather than imported from the table so the hold length does not
 *  depend on whether the chunk landed: the two load states must differ only by
 *  "decoded" vs "literal", never by where a held run ends. `smd-entity-refs.test.ts`
 *  asserts this equals the generated table's own export. */
export const MAX_ENTITY_NAME_LENGTH = 31;

/** The five names XML predefines, and the only ones that decode with no chunk.
 *  `&amp;` and `&lt;` are the spellings escaping turns on, so they never wait on
 *  a network fetch. */
const XML_ENTITIES: Readonly<Record<string, string>> = {
  amp: "&",
  apos: "'",
  gt: ">",
  lt: "<",
  quot: '"',
};

let namedRefs: Readonly<Record<string, string>> = XML_ENTITIES;
let installed = false;
let inFlight: Promise<void> | null = null;

/** CommonMark 6.2: a code point that is not a valid Unicode scalar decodes to
 *  U+FFFD rather than failing. */
function scalar_or_replacement(cp: number): string {
  if (cp === 0 || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) {
    return "\ufffd";
  }
  return String.fromCodePoint(cp);
}

/** Decode a numeric reference body — the `#`-prefixed run between the `&` and the
 *  `;` — or null when it is not one. Consults no table, so this answer is the
 *  same before and after the chunk lands. */
export function decodeNumericRef(body: string): string | null {
  if (body.startsWith("#x") || body.startsWith("#X")) {
    const hex = body.slice(2);
    return hex === "" ? null : scalar_or_replacement(parseInt(hex, 16));
  }
  if (body.startsWith("#")) {
    const dec = body.slice(1);
    return dec === "" ? null : scalar_or_replacement(parseInt(dec, 10));
  }
  return null;
}

/** The character `name` stands for, or null when nothing in the installed table
 *  names it. Null is the stays-literal answer, so a name the absent chunk would
 *  have resolved renders as the text that was typed.
 *
 *  `name` is author text, and both tables are object literals, so the own-member
 *  test is what keeps this total: a bare index answers `&constructor;` with the
 *  inherited `Object` constructor. Owning it here rather than in the tables means
 *  a later table source cannot reintroduce it. */
export function lookupNamedRef(name: string): string | null {
  return Object.hasOwn(namedRefs, name) ? (namedRefs[name] ?? null) : null;
}

/** Whether the full WHATWG table is installed. False means only the five XML
 *  names and the numeric forms decode. */
export function namedEntitiesLoaded(): boolean {
  return installed;
}

/** Fetch and install the full WHATWG table, once per page. NEVER rejects: a
 *  chunk that cannot be fetched leaves the inline map standing for good, so no
 *  caller needs a catch and an awaiting boot cannot wedge on a missing chunk. */
export function entitiesReady(): Promise<void> {
  inFlight ??= import("./smd-entities.js")
    .then((m) => {
      namedRefs = m.NAMED_ENTITIES;
      installed = true;
    })
    .catch(() => {
      /* The inline map stands; see the module comment. */
    });
  return inFlight;
}
