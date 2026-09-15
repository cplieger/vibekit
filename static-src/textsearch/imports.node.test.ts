// The leaf's library question: static-src/textsearch imports nothing from
// vibekit, and nothing from anywhere else either, so the directory can be lifted
// into cplieger/textsearch unchanged. Read off each module's own AST rather than
// asserted by the realm, because a module that imported a DOM-free vibekit helper
// would load fine in any project and every other test would stay green.
//
// Node placement because the sources are a disk read.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { describe, it, expect } from "vitest";

const LEAF = import.meta.dirname;

/** The directory's shipped modules: every .ts that is not a test. */
function leafModules(): string[] {
  return readdirSync(LEAF)
    .filter((f) => f.endsWith(".ts") && !f.includes(".test."))
    .sort();
}

/** Every module specifier `source` reaches: static imports and re-exports,
 *  `import type`, `import("x")` in a type position, and a dynamic `import("x")`
 *  or `require("x")` call anywhere in the file. */
function specifiers(fileName: string, source: string): string[] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS);
  const out: string[] = [];
  const walk = (node: ts.Node): void => {
    if (
      (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
      node.moduleSpecifier !== undefined &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      out.push(node.moduleSpecifier.text);
    } else if (ts.isImportTypeNode(node)) {
      out.push(node.argument.getText(sf).replace(/^["']|["']$/g, ""));
    } else if (
      ts.isCallExpression(node) &&
      (node.expression.kind === ts.SyntaxKind.ImportKeyword ||
        (ts.isIdentifier(node.expression) && node.expression.text === "require"))
    ) {
      const arg = node.arguments[0];
      out.push(arg !== undefined && ts.isStringLiteral(arg) ? arg.text : "<dynamic>");
    }
    node.forEachChild(walk);
  };
  walk(sf);
  return out;
}

describe("static-src/textsearch is a leaf", () => {
  it("holds exactly the three leaf modules", () => {
    expect(leafModules()).toEqual(["copy.ts", "fold.ts", "scan.ts"]);
  });

  it("imports nothing outside its own directory", () => {
    const outside: string[] = [];
    for (const file of leafModules()) {
      for (const spec of specifiers(file, readFileSync(join(LEAF, file), "utf8"))) {
        if (!/^\.\/[^/]+\.js$/.test(spec)) {
          outside.push(`${file}: ${spec}`);
        }
      }
    }
    expect(outside, "a leaf that reaches into vibekit cannot be extracted").toEqual([]);
  });

  it("imports exactly the declared edge list: scan reaches fold, nothing else reaches anything", () => {
    const edges = leafModules().map(
      (file) => `${file} -> ${specifiers(file, readFileSync(join(LEAF, file), "utf8")).join(",")}`,
    );
    expect(edges).toEqual(["copy.ts -> ", "fold.ts -> ", "scan.ts -> ./fold.js"]);
  });

  it("would report an import that reaches out, so the empty list above means something", () => {
    // Guard the guard, in the three shapes a reach-out can take.
    const planted = [
      'import { x } from "../strings.js";',
      'export type { T } from "@cplieger/reactive";',
      'const m = await import("../dom.js");',
      'type R = import("../types.js").Message;',
    ].join("\n");
    expect(specifiers("planted.ts", planted)).toEqual([
      "../strings.js",
      "@cplieger/reactive",
      "../dom.js",
      "../types.js",
    ]);
  });
});
