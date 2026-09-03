// ---------------------------------------------------------------------------
// Forge-related types and metadata.
//
// The frontend works with a unified RepoEntry model that combines
// local clones (from /api/git/repos) and remote repos (from each
// configured forge's /api/forges/{id}/repos endpoint). Entries are
// keyed by ID = "<host>:<owner>/<name>" so the same repo seen
// locally and remotely collapses into one row.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./wire/types.gen.js";

export type { ForgeKind };

/** Human-readable display name for a forge kind. */
export function kindTitle(kind: ForgeKind): string {
  return FORGE_META[kind].title;
}

/** Forge kinds where the host is locked (not user-editable). */
export const HOST_LOCKED_KINDS: readonly ForgeKind[] = ["github", "codeberg"];

/** Default host per forge kind. */
export const DEFAULT_HOST: Record<ForgeKind, string> = {
  github: "github.com",
  gitlab: "gitlab.com",
  codeberg: "codeberg.org",
  gitea: "",
};

/** Centralized metadata for each forge kind. Single source of truth for
 *  display names and icon glyphs. */
export const FORGE_META: Record<
  ForgeKind,
  {
    title: string;
  }
> = {
  github: {
    title: "GitHub",
  },
  gitlab: {
    title: "GitLab",
  },
  codeberg: {
    title: "Codeberg",
  },
  gitea: {
    title: "Gitea / Forgejo",
  },
};

// (RepoEntry interface was removed — was exported but no consumers.
//  If a unified local+remote registry is needed in the future, the
//  shape can be reconstructed from /api/git/repos + /api/forges/.../repos
//  responses or imported from wire/types.gen.ts.)

/** URL template for the account-management page on each forge kind. */
export const FORGE_URLS: Record<ForgeKind, (host: string) => string> = {
  github: (host) => `https://${host}/settings/profile`,
  gitlab: (host) => `https://${host}/-/profile`,
  codeberg: (host) => `https://${host}/user/settings`,
  gitea: (host) => (host === "" ? "" : `https://${host}/user/settings`),
};
