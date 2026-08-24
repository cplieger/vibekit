import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("./confirm.js", () => ({
  confirm: vi.fn(() => Promise.resolve(true)),
}));

const mocks = vi.hoisted(() => ({
  cloneDispatch: vi.fn(),
  deleteDispatch: vi.fn(),
}));

// Override only the two dispatches these rows call; the rest of the module
// stays real so ESM linking resolves every name the graph imports.
vi.mock("./actions/forge.js", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("./actions/forge.js")>();
  return {
    ...orig,
    cloneRepo: { ...orig.cloneRepo, dispatch: mocks.cloneDispatch },
    deleteLocal: { ...orig.deleteLocal, dispatch: mocks.deleteDispatch },
  };
});

import { renderRepoActions, type RepoDeps } from "./forge-auth-repos.js";
import { error as toastError } from "./toast.js";
import type { Repo } from "./wire/types.gen.js";

const KIRO: Repo = {
  owner: "cplieger",
  name: ".kiro",
  full_name: "cplieger/.kiro",
  clone_url: "https://github.com/cplieger/.kiro.git",
};

function deps(): RepoDeps {
  return {
    isCloned: vi.fn(() => false),
    addCloned: vi.fn(),
    removeCloned: vi.fn(),
    bumpState: vi.fn(),
  };
}

function cloneButton(cloned: boolean, d: RepoDeps): HTMLButtonElement {
  const row = renderRepoActions(KIRO, cloned, d);
  document.body.appendChild(row);
  const btn = row.querySelector<HTMLButtonElement>('button[aria-label="Clone into workspace"]');
  if (btn === null) {
    throw new Error("clone button not rendered");
  }
  return btn;
}

describe("repo row clone feedback", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    mocks.cloneDispatch.mockReset();
    mocks.deleteDispatch.mockReset();
    vi.mocked(toastError).mockReset();
  });

  // The reported defect: /api/git/clone answers 200 with {"error": …}, and the
  // reason reached nobody. withAsyncFeedback awaits without rethrowing, so the
  // row showed a spinner, then a ✗ that the next repaint erased.
  it("toasts the server's reason when the clone fails", async () => {
    const reason = "fatal: destination path '.kiro' already exists and is not an empty directory.";
    mocks.cloneDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "success", value: { error: reason } }),
    });

    const d = deps();
    cloneButton(false, d).click();

    await vi.waitFor(() => {
      expect(toastError).toHaveBeenCalledWith(reason);
    });
    expect(d.addCloned).not.toHaveBeenCalled();
  });

  it("toasts a dispatch-level failure", async () => {
    mocks.cloneDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { message: "network down" } }),
    });

    cloneButton(false, deps()).click();

    await vi.waitFor(() => {
      expect(toastError).toHaveBeenCalledWith("network down");
    });
  });

  it("marks the repo cloned and toasts nothing on success", async () => {
    mocks.cloneDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "success", value: { output: "Cloning into '.kiro'..." } }),
    });

    const d = deps();
    cloneButton(false, d).click();

    await vi.waitFor(() => {
      expect(d.addCloned).toHaveBeenCalledWith(".kiro");
    });
    expect(toastError).not.toHaveBeenCalled();
  });
});
