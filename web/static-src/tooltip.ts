// ---------------------------------------------------------------------------
// Styled tooltip system: replaces bare `title` attributes with positioned,
// delay-aware tooltips. First tooltip in a group delays 300ms; subsequent
// peers show instantly while the group is "warm" (500ms cooldown).
//
// Uses pointerover/pointerout (which bubble) rather than
// pointerenter/pointerleave so a single delegated listener on document
// works correctly; event target can be any descendant of the anchor.
// We guard against non-Element targets (Text nodes fire here too) and
// against pointer transitions that stay within the same anchor subtree
// (relatedTarget check). That kills both the "e.target.closest is not
// a function" TypeError and the "tooltip never dismisses" sticking bug.
// ---------------------------------------------------------------------------

// -- State machine ----------------------------------------------------------

type TooltipState =
  | { readonly kind: "idle" }
  | { readonly kind: "pending"; readonly anchor: HTMLElement; readonly timer: ReturnType<typeof setTimeout> }
  | { readonly kind: "visible"; readonly anchor: HTMLElement; readonly tip: HTMLDivElement }
  | { readonly kind: "fading"; readonly tip: HTMLDivElement };

const DELAY_COLD = 1000;
const DELAY_WARM = 0;
const COOLDOWN = 500;

// Module-level counter for unique tooltip element ids (used as the
// target of aria-describedby on the anchor). Resets only on full
// page reload — tooltips are short-lived so collision risk is nil.
let tipIDSeq = 0;

class TooltipController {
  private state: TooltipState = { kind: "idle" };
  private warmUntil = 0;

  init(): void {
    document.addEventListener("pointerover", (e) => { this.onEnter(e); });
    document.addEventListener("pointerout", (e) => { this.onLeave(e); });
    document.addEventListener("focusin", (e) => { this.onEnter(e); });
    document.addEventListener("focusout", (e) => { this.onLeave(e); });
    document.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Escape") this.hide();
    });
    window.addEventListener("blur", () => { this.hide(); });
    document.addEventListener("scroll", () => { this.hide(); }, true);
  }

  private closestAnchor(target: EventTarget | null): HTMLElement | null {
    if (!(target instanceof Element)) return null;
    return target.closest("[data-tooltip]") as HTMLElement | null;
  }

  private onEnter(e: Event): void {
    const target = this.closestAnchor(e.target);
    if (target === null) return;
    const text = target.dataset["tooltip"] ?? "";
    if (text === "") return;

    if (this.state.kind === "pending" && this.state.anchor === target) return;
    if (this.state.kind === "visible" && this.state.anchor === target) return;

    this.teardown();
    const delay = Date.now() < this.warmUntil ? DELAY_WARM : DELAY_COLD;
    const timer = setTimeout(() => { this.show(target, text); }, delay);
    this.state = { kind: "pending", anchor: target, timer };
  }

  private onLeave(e: Event): void {
    const target = this.closestAnchor(e.target);
    if (target === null) return;

    if (this.state.kind === "pending" && this.state.anchor !== target) return;
    if (this.state.kind === "visible" && this.state.anchor !== target) return;
    if (this.state.kind === "idle" || this.state.kind === "fading") return;

    const pe = e as Event & { relatedTarget?: EventTarget | null };
    const related = pe.relatedTarget ?? null;
    if (related instanceof Node && target.contains(related)) return;

    this.hide();
  }

  private show(anchor: HTMLElement, text: string): void {
    if (!anchor.isConnected) { this.state = { kind: "idle" }; return; }
    this.teardown();

    const tip = document.createElement("div");
    tip.className = "vk-tooltip";
    tip.textContent = text;
    tip.setAttribute("role", "tooltip");
    // Generate a unique id so screen readers can associate the
    // tooltip with the anchor via aria-describedby. Without this the
    // tooltip is purely visual — AT users never hear the content.
    const tipID = `vk-tip-${++tipIDSeq}`;
    tip.id = tipID;
    document.body.appendChild(tip);
    anchor.setAttribute("aria-describedby", tipID);

    const rect = anchor.getBoundingClientRect();
    const tipRect = tip.getBoundingClientRect();
    let left = rect.left + rect.width / 2 - tipRect.width / 2;
    let top = rect.top - tipRect.height - 6;

    if (left < 4) left = 4;
    if (left + tipRect.width > window.innerWidth - 4) left = window.innerWidth - 4 - tipRect.width;
    if (top < 4) { top = rect.bottom + 6; }

    tip.style.left = `${String(left)}px`;
    tip.style.top = `${String(top)}px`;

    this.state = { kind: "visible", anchor, tip };
    // Warm window covers DELAY_COLD so hovering a sibling anchor shows instantly
    // (cross-anchor transition). The hide() assignments reset to true COOLDOWN.
    this.warmUntil = Date.now() + COOLDOWN + DELAY_COLD;
  }

  private hide(): void {
    if (this.state.kind === "pending") {
      clearTimeout(this.state.timer);
      this.state = { kind: "idle" };
      this.warmUntil = Date.now() + COOLDOWN;
      return;
    }

    if (this.state.kind === "visible") {
      const tip = this.state.tip;
      // Drop the aria-describedby pointer before fade so AT doesn't
      // re-announce the tooltip while it animates out.
      this.state.anchor.removeAttribute("aria-describedby");
      tip.classList.add("fading-out");
      this.state = { kind: "fading", tip };

      tip.addEventListener("transitionend", () => {
        if (this.state.kind === "fading" && this.state.tip === tip) this.state = { kind: "idle" };
        tip.remove();
      }, { once: true });
      setTimeout(() => {
        if (this.state.kind === "fading" && this.state.tip === tip) this.state = { kind: "idle" };
        if (tip.isConnected) tip.remove();
      }, 100);

      this.warmUntil = Date.now() + COOLDOWN;
      return;
    }
  }

  private teardown(): void {
    switch (this.state.kind) {
      case "idle":
        break;
      case "pending":
        clearTimeout(this.state.timer);
        break;
      case "visible":
        this.state.anchor.removeAttribute("aria-describedby");
        this.state.tip.remove();
        break;
      case "fading":
        this.state.tip.remove();
        break;
    }
    this.state = { kind: "idle" };
  }
}

const instance = new TooltipController();

let initialized = false;

export function initTooltips(): void {
  if (initialized) return;
  initialized = true;
  instance.init();
}
