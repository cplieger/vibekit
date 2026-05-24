// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Each test re-imports the module to reset its module-level state.
beforeEach(() => {
  document.body.innerHTML = "";
  vi.useFakeTimers();
  vi.resetModules();
});

afterEach(() => {
  vi.useRealTimers();
});

function getStack(): HTMLElement | null {
  return document.querySelector(".vk-toast-stack");
}

function toasts(): NodeListOf<Element> {
  return document.querySelectorAll(".vk-toast");
}

async function loadToast(): Promise<typeof import("./toast.js")> {
  return await import("./toast.js");
}

// Force the entry-frame requestAnimationFrame to fire synchronously.
function flushRaf(): void {
  // happy-dom resolves rAFs after a microtask + 16ms timer; advance
  // a frame to be safe.
  vi.advanceTimersByTime(20);
}

// Simulate the transitionend event used by dismiss() to clean up.
function endTransitions(): void {
  for (const t of toasts()) {
    t.dispatchEvent(new Event("transitionend", { bubbles: true }));
    Object.defineProperty(new Event("transitionend"), "target", { value: t });
  }
}

describe("toast — basic rendering", () => {
  it("info creates a toast with vk-toast-info class", async () => {
    const { info } = await loadToast();
    info("hello");
    const t = document.querySelector(".vk-toast");
    expect(t).not.toBeNull();
    expect(t?.classList.contains("vk-toast-info")).toBe(true);
    expect(t?.textContent).toContain("hello");
  });

  it("success creates a toast with vk-toast-success class", async () => {
    const { success } = await loadToast();
    success("ok");
    const t = document.querySelector(".vk-toast");
    expect(t?.classList.contains("vk-toast-success")).toBe(true);
  });

  it("error creates a toast with vk-toast-error class + role=alert", async () => {
    const { error } = await loadToast();
    error("boom");
    const t = document.querySelector(".vk-toast");
    expect(t?.classList.contains("vk-toast-error")).toBe(true);
    expect(t?.getAttribute("role")).toBe("alert");
  });

  it("stack has role=status + aria-live=polite", async () => {
    const { info } = await loadToast();
    info("hi");
    const stack = getStack();
    expect(stack?.getAttribute("role")).toBe("status");
    expect(stack?.getAttribute("aria-live")).toBe("polite");
  });

  it("info/success include a progress bar; error does not", async () => {
    const { info, success, error, _resetForTest } = await loadToast();
    info("a");
    expect(toasts()[0]?.querySelector(".vk-toast-progress")).not.toBeNull();
    _resetForTest();
    success("b");
    expect(toasts()[0]?.querySelector(".vk-toast-progress")).not.toBeNull();
    _resetForTest();
    error("c");
    expect(toasts()[0]?.querySelector(".vk-toast-progress")).toBeNull();
  });

  it("toast text uses textContent, not HTML", async () => {
    const { info } = await loadToast();
    info("<script>alert(1)</script>");
    const msg = document.querySelector(".vk-toast-msg");
    // textContent of the rendered text equals the literal string.
    expect(msg?.textContent).toBe("<script>alert(1)</script>");
    // No actual <script> tag was created.
    expect(document.querySelector(".vk-toast script")).toBeNull();
  });
});

describe("toast — auto-dismiss", () => {
  it("auto-dismisses info after 4s", async () => {
    const { info } = await loadToast();
    info("hi");
    flushRaf();
    expect(toasts().length).toBe(1);
    vi.advanceTimersByTime(4000);
    // Trigger the cleanup fallback (transitionend may not fire in jsdom-ish env).
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("error does not auto-dismiss (sticky)", async () => {
    const { error } = await loadToast();
    error("fail");
    flushRaf();
    vi.advanceTimersByTime(60000);
    expect(toasts().length).toBe(1);
  });

  it("showToast accepts an explicit duration", async () => {
    const { showToast } = await loadToast();
    showToast("slow", "info", 10000);
    flushRaf();
    vi.advanceTimersByTime(4000);
    expect(toasts().length).toBe(1);  // still there past info default
    vi.advanceTimersByTime(7000);
    expect(toasts().length).toBe(0);
  });

  it("showToast(msg, 'error') with no duration is sticky", async () => {
    const { showToast } = await loadToast();
    showToast("explicit", "error");
    flushRaf();
    vi.advanceTimersByTime(60000);
    expect(toasts().length).toBe(1);
  });
});

describe("toast — dismissal", () => {
  it("click dismisses the toast", async () => {
    const { info } = await loadToast();
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    t.click();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("Escape dismisses the most recent toast (LIFO)", async () => {
    const { info } = await loadToast();
    info("a");
    info("b");
    flushRaf();
    expect(toasts().length).toBe(2);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(1);
    expect(toasts()[0]?.textContent).toContain("a");
  });

  it("returns a manual dismiss function", async () => {
    const { info } = await loadToast();
    const close = info("hi");
    flushRaf();
    expect(toasts().length).toBe(1);
    close();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("returned dismiss is idempotent", async () => {
    const { info } = await loadToast();
    const close = info("hi");
    flushRaf();
    close();
    close();  // safe to call again
    close();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });
});

describe("toast — pause on hover/focus", () => {
  it("mouseenter pauses the timer; mouseleave resumes", async () => {
    const { info } = await loadToast();
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    vi.advanceTimersByTime(2000);  // half the duration elapsed
    t.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(60000);  // long pause
    expect(toasts().length).toBe(1);  // still visible
    t.dispatchEvent(new MouseEvent("mouseleave"));
    vi.advanceTimersByTime(2100);   // remaining ~2s + cleanup margin
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("focusin pauses; focusout resumes", async () => {
    const { info } = await loadToast();
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    vi.advanceTimersByTime(2000);
    t.dispatchEvent(new FocusEvent("focusin"));
    vi.advanceTimersByTime(60000);
    expect(toasts().length).toBe(1);
    t.dispatchEvent(new FocusEvent("focusout"));
    vi.advanceTimersByTime(2100);
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });
});

describe("toast — queue + max-visible", () => {
  it("queues 4th toast when 3 are visible", async () => {
    const { info } = await loadToast();
    info("1"); info("2"); info("3"); info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    // The visible ones are the first 3.
    const texts = [...toasts()].map((t) => t.textContent);
    expect(texts.some((x) => x?.includes("1"))).toBe(true);
    expect(texts.some((x) => x?.includes("2"))).toBe(true);
    expect(texts.some((x) => x?.includes("3"))).toBe(true);
    expect(texts.some((x) => x?.includes("4"))).toBe(false);
  });

  it("promotes queued toast on dismiss", async () => {
    const { info } = await loadToast();
    info("1"); info("2"); info("3"); info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    (toasts()[0] as HTMLElement).click();
    vi.advanceTimersByTime(500);
    flushRaf();
    expect(toasts().length).toBe(3);
    const texts = [...toasts()].map((t) => t.textContent);
    expect(texts.some((x) => x?.includes("4"))).toBe(true);
  });

  it("dismissing a queued toast (before mount) removes it from queue", async () => {
    const { info } = await loadToast();
    info("1"); info("2"); info("3");
    const close4 = info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    // Dismiss the queued one before any visible one drops.
    close4();
    // Now dismiss a visible one — nothing should promote (queue empty).
    (toasts()[0] as HTMLElement).click();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(2);
  });
});

describe("toast — accessibility attributes", () => {
  it("toast is keyboard-focusable (tabindex=0)", async () => {
    const { info } = await loadToast();
    info("hi");
    const t = document.querySelector(".vk-toast");
    expect(t?.getAttribute("tabindex")).toBe("0");
  });

  it("progress bar is aria-hidden", async () => {
    const { info } = await loadToast();
    info("hi");
    const bar = document.querySelector(".vk-toast-progress");
    expect(bar?.getAttribute("aria-hidden")).toBe("true");
  });
});
