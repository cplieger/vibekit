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
});
