// ---------------------------------------------------------------------------
// Arrow key navigation for composite widgets (WAI-ARIA roving tabindex).
//
// Call wireArrowNav(container, selector) to enable Up/Down/Home/End
// navigation within a container. Enter/Space activates the focused item.
// ---------------------------------------------------------------------------

/** Wire arrow key navigation on a container's children matching selector. */
export function wireArrowNav(
  container: HTMLElement,
  selector: string,
  options: { orientation?: "vertical" | "horizontal" } = {},
): void {
  const isVertical = options.orientation !== "horizontal";
  const prevKey = isVertical ? "ArrowUp" : "ArrowLeft";
  const nextKey = isVertical ? "ArrowDown" : "ArrowRight";

  container.addEventListener("keydown", (e: KeyboardEvent) => {
    const items = [...container.querySelectorAll<HTMLElement>(selector)];
    if (items.length === 0) {
      return;
    }
    const current = document.activeElement as HTMLElement | null;
    const idx = current !== null ? items.indexOf(current) : -1;

    let next = -1;
    switch (e.key) {
      case nextKey:
        next = idx < items.length - 1 ? idx + 1 : 0;
        break;
      case prevKey:
        next = idx > 0 ? idx - 1 : items.length - 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      case "Enter":
      case " ":
        if (current !== null && items.includes(current)) {
          e.preventDefault();
          current.click();
        }
        return;
      default:
        return;
    }
    if (next >= 0 && next < items.length) {
      e.preventDefault();
      items[next]!.focus(); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
  });

  // Make items focusable via roving tabindex.
  const items = container.querySelectorAll<HTMLElement>(selector);
  items.forEach((item, i) => {
    item.setAttribute("tabindex", i === 0 ? "0" : "-1");
  });
  container.addEventListener("focusin", (e: FocusEvent) => {
    const target = e.target as HTMLElement;
    if (!target.matches(selector)) {
      return;
    }
    for (const item of container.querySelectorAll<HTMLElement>(selector)) {
      item.setAttribute("tabindex", item === target ? "0" : "-1");
    }
  });
}
