// Structural guard: every credential input in static/index.html sits inside a
// <form>.
//
// Chromium logs "[DOM] Password field is not contained in a form" for a
// `type="password"` outside one, and the reason is not cosmetic: nothing tells
// the browser which fields belong together, which one is the identifier, or
// where the submit is, so its credential handling has no shape to reason about.
// The MCP remote panel shipped its OAuth client secret that way — a bare <div>
// with a `type="button"` Save — and the warning was reproducible on every page
// load.
//
// Read from the markup rather than the module, because the shape IS the markup:
// mcp-panels.ts resolves its fields by id and would go on working inside a div.
// A second password field added to a panel that is still a div is exactly the
// regression this catches.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

function parsed(): Document {
  return new DOMParser().parseFromString(indexHtml, "text/html");
}

describe("credential inputs in static/index.html", () => {
  it("puts every password field inside a form", () => {
    const doc = parsed();
    const fields = [...doc.querySelectorAll<HTMLInputElement>('input[type="password"]')];
    // Guards the guard: a selector that stops matching would make every
    // assertion below vacuous.
    expect(fields.length).toBeGreaterThan(0);
    for (const field of fields) {
      expect(field.closest("form"), `${field.id} is not in a form`).not.toBeNull();
    }
  });

  it("keeps the OAuth client secret out of the browser's password manager", () => {
    // These are a SERVER's OAuth credentials, not the reader's own login, so the
    // field asks not to be autofilled and its form asks not to be remembered.
    const doc = parsed();
    const secret = doc.querySelector<HTMLInputElement>("#mcp-remote-oauth-client-secret");
    expect(secret?.getAttribute("autocomplete")).toBe("new-password");
    expect(secret?.closest("form")?.getAttribute("autocomplete")).toBe("off");
  });

  it("gives the credential form a real submit, so Enter reaches Save", () => {
    // A form whose only button is `type="button"` has no submit at all: the
    // password field is in a form the browser still cannot submit, and Enter in
    // any field does nothing.
    const doc = parsed();
    const form = doc
      .querySelector<HTMLFormElement>("#mcp-remote-oauth-client-secret")
      ?.closest("form");
    expect(form?.querySelector('button[type="submit"]')?.id).toBe("mcp-remote-save");
  });

  it("leaves validation to the panel, which renders its own field errors", () => {
    // The URL field is `type="url"`, so without novalidate a native bubble would
    // fire beside the server's inline field marking — two validation surfaces
    // disagreeing about one form.
    const doc = parsed();
    const form = doc
      .querySelector<HTMLFormElement>("#mcp-remote-oauth-client-secret")
      ?.closest("form");
    expect(form?.hasAttribute("novalidate")).toBe(true);
  });
});
