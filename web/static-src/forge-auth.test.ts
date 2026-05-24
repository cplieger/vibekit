// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(() => Promise.resolve(null)),
  apiPut: vi.fn(() => Promise.resolve(null)),
  apiDelete: vi.fn(() => Promise.resolve(null)),
}));

import { renderForgesPanel } from "./forge-auth.js";
import { apiGet } from "./api-client.js";

const mockedApiGet = vi.mocked(apiGet);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel") as HTMLElement;
}

describe("forge-auth: 4-section layout", () => {
  beforeEach(() => {
    setupDOM();
    vi.clearAllMocks();
  });

  it("renders one section per supported forge kind", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const sections = panel().querySelectorAll<HTMLElement>(".forge-kind-section");
    expect(sections.length).toBe(4);
    const kinds = [...sections].map((s) => s.dataset["kind"]);
    expect(kinds).toEqual(["github", "gitlab", "codeberg", "gitea"]);
  });

  it("each section renders an empty state and an Add account button when no accounts", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const s of panel().querySelectorAll<HTMLElement>(".forge-kind-section")) {
      expect(s.querySelector(".forge-account-empty")).not.toBeNull();
      expect(s.querySelector("[data-forge-add]")).not.toBeNull();
    }
  });

  it("only the GitHub section gets the 'Add a PAT' button", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const ghPat = panel().querySelector(".forge-kind-section[data-kind='github'] [data-forge-add-pat]");
    expect(ghPat).not.toBeNull();
    for (const k of ["gitlab", "codeberg", "gitea"]) {
      const pat = panel().querySelector(`.forge-kind-section[data-kind='${k}'] [data-forge-add-pat]`);
      expect(pat).toBeNull();
    }
  });

  it("renders one slim row per connected account, prefers email over username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", email: "alice@example.com", connected: true },
        { id: "gitlab:gitlab.com", kind: "gitlab", host: "gitlab.com", username: "bob", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();

    const ghRow = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='github'] .forge-account-row");
    expect(ghRow).not.toBeNull();
    expect(ghRow!.querySelector(".forge-account-primary")?.textContent).toBe("alice@example.com");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("@alice");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("github.com");

    const glRow = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab'] .forge-account-row");
    expect(glRow!.querySelector(".forge-account-primary")?.textContent).toBe("bob");
  });

  it("each account row has a Manage link and a Sign out button", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", email: "a@x.io", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    const manage = row.querySelector<HTMLAnchorElement>(".forge-account-manage");
    const signOut = [...row.querySelectorAll<HTMLButtonElement>("button")]
      .find((b) => b.textContent === "Sign out");
    expect(manage).not.toBeNull();
    expect(manage!.href).toContain("github.com");
    expect(manage!.href).toContain("settings/profile");
    expect(signOut).toBeDefined();
  });

  it("shows error styling and last_error text on a disconnected account", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", connected: false, last_error: "token expired" },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.classList.contains("forge-account-row-error")).toBe(true);
    expect(row.querySelector(".forge-account-error")?.textContent).toBe("token expired");
  });

  it("renders a list error message when /api/forges fails", async () => {
    mockedApiGet.mockResolvedValueOnce(null);
    await renderForgesPanel();
    expect(panel().querySelector(".forge-error")).not.toBeNull();
  });

  it("renders an SVG icon (not a letter) in each kind badge", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const badge = panel().querySelector<HTMLElement>(`.forge-kind-section[data-kind='${k}'] .forge-kind-badge`);
      expect(badge).not.toBeNull();
      const svg = badge!.querySelector("svg");
      expect(svg, `${k} badge should contain an svg`).not.toBeNull();
      expect(svg!.getAttribute("viewBox")).toBe("0 0 24 24");
      // No leftover letter text inside the badge.
      expect(badge!.textContent?.trim() ?? "").toBe("");
    }
  });

  it("PAT form hides the host field for github + codeberg, shows it for gitlab + gitea", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    // Open every PAT form: github via "Add a PAT", others via "Add account".
    const ghPat = panel().querySelector<HTMLButtonElement>(".forge-kind-section[data-kind='github'] [data-forge-add-pat]");
    ghPat!.click();
    for (const k of ["gitlab", "codeberg", "gitea"]) {
      const add = panel().querySelector<HTMLButtonElement>(`.forge-kind-section[data-kind='${k}'] [data-forge-add]`);
      add!.click();
    }
    const inputs: Record<string, HTMLInputElement | null> = {};
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const form = panel().querySelector<HTMLFormElement>(`.forge-kind-section[data-kind='${k}'] form.forge-pat-form`);
      expect(form, `${k} PAT form should be open`).not.toBeNull();
      inputs[k] = form!.querySelector<HTMLInputElement>("input");
    }
    expect(inputs["github"]!.type).toBe("hidden");
    expect(inputs["github"]!.value).toBe("github.com");
    expect(inputs["codeberg"]!.type).toBe("hidden");
    expect(inputs["codeberg"]!.value).toBe("codeberg.org");
    expect(inputs["gitlab"]!.type).toBe("text");
    expect(inputs["gitea"]!.type).toBe("text");
    expect(inputs["gitea"]!.value).toBe("");
    expect(inputs["gitea"]!.placeholder).toBe("your-host.example.com");
  });

  it("the gitea section is labeled 'Gitea / Forgejo'", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const title = panel().querySelector(".forge-kind-section[data-kind='gitea'] .forge-kind-title")?.textContent;
    expect(title).toBe("Gitea / Forgejo");
  });

  it("does not duplicate @username in meta when the primary line is already the username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        // Account with no email — primary should be the username, meta should NOT add @cplieger.
        { id: "github:github.com", kind: "github", host: "github.com", username: "cplieger", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.querySelector(".forge-account-primary")?.textContent).toBe("cplieger");
    const meta = row.querySelector(".forge-account-meta")?.textContent ?? "";
    expect(meta).not.toContain("@cplieger");
    expect(meta).toBe("github.com");
  });

  it("clicking 'Add account' twice toggles the form open then closed", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab']")!;
    const btn = section.querySelector<HTMLButtonElement>("[data-forge-add]")!;
    const slot = section.querySelector<HTMLElement>("[data-forge-slot]")!;
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "first click opens").not.toBeNull();
    expect(slot.dataset["mode"]).toBe("add");
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "second click closes").toBeNull();
    expect(slot.dataset["mode"]).toBeUndefined();
  });

  it("clicking 'Add a PAT' twice toggles the GitHub PAT form open then closed", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='github']")!;
    const btn = section.querySelector<HTMLButtonElement>("[data-forge-add-pat]")!;
    const slot = section.querySelector<HTMLElement>("[data-forge-slot]")!;
    btn.click();
    expect(slot.querySelector("form.forge-pat-form")).not.toBeNull();
    expect(slot.dataset["mode"]).toBe("pat");
    btn.click();
    expect(slot.querySelector("form.forge-pat-form")).toBeNull();
    expect(slot.dataset["mode"]).toBeUndefined();
  });
});
