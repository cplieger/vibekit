// A session that holds a message WINDOW holds the base that window is numbered
// from — asserted over the source rather than over one path's behaviour, because
// the defect this guards is a WRITER forgetting, and the next writer is the one
// nobody has written yet.
//
// It has happened twice. `loadList` rebuilds a whole `Session` from a chat header
// and had to remember to carry the base across (review round 2, finding 1);
// `toProvisionalSession` built its row and took its window through a separate
// `upsertMessage` loop, so neither call held both and the pre-network paint of a
// paged chat numbered a partial tail from #1. Both have behavioural tests
// elsewhere. What no behavioural test can cover is a FOURTH rebuild site, so this
// one enumerates them mechanically and fails on any that carries a window with no
// base beside it.
//
// The fingerprint is `working_label` plus `message_count`: both are required
// `Session` fields with no default, so only a whole rebuild writes them, and a
// spread-update (`{ ...s, working_label: … }`) carries just one. So the guard is NOT
// total — a window written without a rebuild (`session.messages = page`, or a spread
// carrying `messages`) escapes it. The only two are `store-load.ts` `loadMessages`'s
// prepend and replace, both LEFT-edge, paired by `pageStartsWindow` → `adoptTurnBase`.
//
// One accepted false positive: a site carrying its base through a HELPER
// (`...baseFieldsFor(x)`) reads as a violation here, because the field names are
// what a reviewer greps for. Inline them, or state the exemption in this test.

import { describe, it, expect } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const here = dirname(fileURLToPath(import.meta.url));

/** Every production TypeScript module under `static-src`. Tests are excluded: they
 *  build sessions as fixtures, which is not a rebuild path the app takes. */
function productionSources(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === "__test-helpers__") {
        continue;
      }
      out.push(...productionSources(path));
      continue;
    }
    if (entry.name.endsWith(".test.ts") || !entry.name.endsWith(".ts")) {
      continue;
    }
    out.push(path);
  }
  return out;
}

interface Rebuild {
  /** `static-src`-relative file, which is what the population assertion compares —
   *  a line number rots on the next unrelated edit above the literal. */
  readonly file: string;
  /** File and line, for a failure to name the site. */
  readonly at: string;
  /** Whether the literal's window is a fresh empty array, which needs no base. */
  readonly emptyWindow: boolean;
  readonly hasOffset: boolean;
  readonly hasClosed: boolean;
}

/** Property names this object literal states, at any depth reachable without
 *  leaving it — so a conditional spread of a nested literal counts, which is how
 *  both real sites state the base under `exactOptionalPropertyTypes`. */
function statedKeys(lit: ts.ObjectLiteralExpression): Set<string> {
  const keys = new Set<string>();
  const visit = (node: ts.Node): void => {
    if (
      (ts.isPropertyAssignment(node) || ts.isShorthandPropertyAssignment(node)) &&
      (ts.isIdentifier(node.name) || ts.isStringLiteral(node.name))
    ) {
      keys.add(node.name.text);
    }
    ts.forEachChild(node, visit);
  };
  for (const prop of lit.properties) {
    visit(prop);
  }
  return keys;
}

/** The literal's `messages` initializer, or undefined for a shorthand — which
 *  cannot be read here and is therefore treated as a window that may be full. */
function messagesInitializer(lit: ts.ObjectLiteralExpression): ts.Expression | undefined {
  for (const prop of lit.properties) {
    if (
      ts.isPropertyAssignment(prop) &&
      ts.isIdentifier(prop.name) &&
      prop.name.text === "messages"
    ) {
      return prop.initializer;
    }
  }
  return undefined;
}

function rebuildsIn(path: string): Rebuild[] {
  const source = readFileSync(path, "utf8");
  const sf = ts.createSourceFile(path, source, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS);
  const file = relative(here, path);
  const found: Rebuild[] = [];
  const walk = (node: ts.Node): void => {
    if (ts.isObjectLiteralExpression(node)) {
      const keys = statedKeys(node);
      if (keys.has("working_label") && keys.has("message_count")) {
        const init = messagesInitializer(node);
        const line = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
        found.push({
          file,
          at: `${file}:${String(line)}`,
          emptyWindow:
            init !== undefined && ts.isArrayLiteralExpression(init) && init.elements.length === 0,
          hasOffset: keys.has("turn_offset"),
          hasClosed: keys.has("turn_segment_closed"),
        });
      }
    }
    ts.forEachChild(node, walk);
  };
  ts.forEachChild(sf, walk);
  return found;
}

const rebuilds = productionSources(here).flatMap(rebuildsIn);

describe("the session-rebuild sites", () => {
  it("are the three this guard was measured against", () => {
    // The trip-wire, and the reason it is an equality rather than a floor: a fourth
    // site is a new writer of the base, and adding one must force whoever adds it to
    // read the rule below rather than inherit it silently.
    expect(rebuilds.map((r) => r.file).sort()).toEqual([
      "boot-snapshot.ts",
      "store-load.ts",
      "store.ts",
    ]);
  });

  it("state the window base wherever the window can hold messages", () => {
    const offenders = rebuilds
      .filter((r) => !r.emptyWindow && !(r.hasOffset && r.hasClosed))
      .map((r) => r.at);

    expect(offenders).toEqual([]);
  });

  it("state BOTH halves of the base or neither", () => {
    // `store-load.ts` `adoptTurnBase` deletes both when either is missing, because a
    // half-present base is a stripped field rather than a partial fact. A rebuild
    // stating one half would put exactly that shape back on a session.
    const halves = rebuilds.filter((r) => r.hasOffset !== r.hasClosed).map((r) => r.at);

    expect(halves).toEqual([]);
  });

  it("exempt only a window built EMPTY, and that one states no base", () => {
    // The exemption is why the guard is not simply "every rebuild states the base": a
    // row built from a header holds no window, and `evictChatMessages` DELETES the
    // base for that case, so stating one there would be the inverse defect. It has to
    // be an empty ARRAY LITERAL, never a carried-over window.
    const exempt = rebuilds.filter((r) => r.emptyWindow);

    expect(exempt.map((r) => r.file)).toEqual(["store.ts"]);
    expect(exempt.filter((r) => r.hasOffset || r.hasClosed).map((r) => r.at)).toEqual([]);
  });
});
