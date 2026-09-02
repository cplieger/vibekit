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

describe("batch clone failure toast", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    mocks.cloneDispatch.mockReset();
    vi.mocked(toastError).mockReset();
  });

  function repo(name: string): Repo {
    return {
      owner: "cplieger",
      name,
      full_name: `cplieger/${name}`,
      clone_url: `https://github.com/cplieger/${name}.git`,
    };
  }

  // A bare count ("1 of 63 failed") left the user diffing 63 directories to
  // find the one missing repo; the toast must NAME what failed.
  it("names the one repo that failed", async () => {
    const { cloneAllForAccount } = await import("./forge-auth-repos.js");
    mocks.cloneDispatch.mockImplementation(({ url }: { url: string }) =>
      Promise.resolve(url.includes("/loki.git") ? { error: "signal: killed" } : {}),
    );
    const btn = document.createElement("button");
    await cloneAllForAccount([repo("alpha"), repo("loki"), repo("beta")], btn, deps());
    expect(toastError).toHaveBeenCalledWith("Clone failed for loki (1 of 3 repos)");
  });

  it("caps the named repos at three and counts the rest", async () => {
    const { cloneFailureToast } = await import("./forge-auth-repos.js");
    expect(cloneFailureToast(["a", "b", "c", "d", "e"], 63)).toBe(
      "Clone failed for a, b, c and 2 more (5 of 63 repos)",
    );
  });

  it("shows git's percent on the button while a repo transfers", async () => {
    const { cloneAllForAccount } = await import("./forge-auth-repos.js");
    const btn = document.createElement("button");
    let duringProgress = "";
    mocks.cloneDispatch.mockImplementation(
      ({ onProgress }: { onProgress?: (line: string) => void }) => {
        onProgress?.("Receiving objects:  42% (215/511)");
        duringProgress = btn.textContent ?? "";
        return Promise.resolve({});
      },
    );
    await cloneAllForAccount([repo("loki")], btn, deps());
    expect(duringProgress).toBe("Cloning 1/1 (42%)…");
  });

  it("toasts nothing when every clone lands", async () => {
    const { cloneAllForAccount } = await import("./forge-auth-repos.js");
    mocks.cloneDispatch.mockResolvedValue({});
    const btn = document.createElement("button");
    await cloneAllForAccount([repo("alpha"), repo("beta")], btn, deps());
    expect(toastError).not.toHaveBeenCalled();
  });
});
