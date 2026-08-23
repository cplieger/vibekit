// Structural guard for the expandable-pill markup (static/index.html).
//
// Every expanded card is a SIBLING of its trigger, never a descendant of it.
// Two things break the moment a card is nested back inside its button:
//
//   1. The universal press scale (`:active { transform: scale(0.96) }`,
//      03-base.css) shrinks the open menu along with its trigger. The
//      workaround that lived here before pressed the pill's CHILDREN and
//      pinned the pill itself to `transform: none`, so a pressed trigger's
//      border and background did not move at all.
//   2. A card holding buttons (the model list, the mode list) puts
//      interactive content inside a <button>: invalid HTML, and assistive
//      tech flattens it.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

// Parse only the two regions that hold expandable pills, rather than the whole
// document: a full-document parse would make the runner chase the
// <link rel=stylesheet> over the network.
function slice(html: string, from: string, to: string): HTMLElement {
  const start = html.indexOf(from);
  const end = html.indexOf(to, start + 1);
  expect(start, `marker not found: ${from}`).toBeGreaterThan(-1);
  expect(end, `marker not found: ${to}`).toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = html.slice(start, end);
  return host;
}

describe("expandable pill markup (static/index.html)", () => {
  it("puts every expand card beside its trigger, never inside a button", () => {
    const regions = [
      slice(indexHtml, '<div class="prompt-pills">', "</form>"),
      slice(indexHtml, '<div class="sidebar-footer">', '<a id="user-email"'),
    ];

    let cards = 0;
    for (const region of regions) {
      for (const card of Array.from(region.querySelectorAll<HTMLElement>(".pill-expand-content"))) {
        cards++;
        const where = card.id === "" ? (card.className ?? "") : `#${card.id}`;
        expect(card.closest("button"), `${where} must not sit inside a <button>`).toBeNull();
        const trigger = card.previousElementSibling;
        expect(
          trigger?.classList.contains("pill-expandable"),
          `${where} must follow its .pill-expandable trigger`,
        ).toBe(true);
      }
    }
    // The context, model, mode, chat-options, task-list and status cards.
    expect(cards).toBe(6);
  });

  it("gives every expandable trigger a card next to it", () => {
    const regions = [
      slice(indexHtml, '<div class="prompt-pills">', "</form>"),
      slice(indexHtml, '<div class="sidebar-footer">', '<a id="user-email"'),
    ];

    for (const region of regions) {
      for (const trigger of Array.from(region.querySelectorAll<HTMLElement>(".pill-expandable"))) {
        expect(
          trigger.nextElementSibling?.classList.contains("pill-expand-content"),
          `#${trigger.id} must be followed by its .pill-expand-content card`,
        ).toBe(true);
      }
    }
  });
});
