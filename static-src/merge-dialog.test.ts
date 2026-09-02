import { describe, it, expect, vi, beforeEach } from "vitest";

const mocks = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  patchDispatch: vi.fn(),
}));

vi.mock("./persist.js", () => ({ loadSettings: mocks.loadSettings }));
vi.mock("./actions/settings.js", () => ({
  patchAppSettings: { dispatch: mocks.patchDispatch },
}));

import { openMergeMethodDialog } from "./merge-dialog.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

/** The static dialog markup from index.html, reduced to what the module
 *  resolves: the ids, the radio group and the close hooks. */
function mountDialog(): void {
  document.body.innerHTML = `
    <dialog id="pr-merge-dialog">
      <h3 id="pr-merge-title"></h3>
      <button type="button" data-pr-merge-close>x</button>
      <p id="pr-merge-message"></p>
      <label><input type="radio" name="pr-merge-method" value="rebase"></label>
      <label><input type="radio" name="pr-merge-method" value="squash"></label>
      <button type="button" data-pr-merge-close>Cancel</button>
      <button type="button" id="pr-merge-confirm-btn">Merge</button>
    </dialog>`;
}

function checkedValue(): string | undefined {
  return document.querySelector<HTMLInputElement>('input[name="pr-merge-method"]:checked')?.value;
}

function confirmBtn(): HTMLButtonElement {
  const btn = document.getElementById("pr-merge-confirm-btn") as HTMLButtonElement | null;
  if (btn === null) {
    throw new Error("confirm button missing");
  }
  return btn;
}

const OPTS = { title: "Merge pull request", message: "PR #7", confirmLabel: "Merge" };

describe("merge method dialog", () => {
  beforeEach(() => {
    mocks.loadSettings.mockReset();
    mocks.patchDispatch.mockReset();
    mountDialog();
  });

  it("preselects the server-remembered method", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "squash" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("squash");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
  });

  it("falls back to rebase when nothing was ever picked", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload());
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("rebase");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("rebase");
  });

  it("falls back to rebase when the settings fetch fails", async () => {
    mocks.loadSettings.mockResolvedValue(null);
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("rebase");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("rebase");
  });

  it("persists a changed pick as the next default", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "rebase" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("rebase");
    });
    const squash = document.querySelector<HTMLInputElement>('input[value="squash"]');
    squash!.checked = true;
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
    expect(mocks.patchDispatch).toHaveBeenCalledWith({ body: { last_merge_method: "squash" } });
  });

  it("does not re-persist an unchanged pick", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "squash" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("squash");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
    expect(mocks.patchDispatch).not.toHaveBeenCalled();
  });

  it("resolves null on cancel and merges nothing", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload());
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("rebase");
    });
    const cancel = document.querySelector<HTMLButtonElement>("[data-pr-merge-close]");
    cancel!.click();
    await expect(p).resolves.toBeNull();
    expect(mocks.patchDispatch).not.toHaveBeenCalled();
  });

  it("renders the caller's title, message and confirm label", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload());
    const p = openMergeMethodDialog({
      title: "Merge when green",
      message: "PR #9 — fix: x",
      confirmLabel: "Arm auto-merge",
    });
    await vi.waitFor(() => {
      expect(document.getElementById("pr-merge-confirm-btn")?.textContent).toBe("Arm auto-merge");
    });
    expect(document.getElementById("pr-merge-title")?.textContent).toBe("Merge when green");
    expect(document.getElementById("pr-merge-message")?.textContent).toBe("PR #9 — fix: x");
    confirmBtn().click();
    await p;
  });
});
