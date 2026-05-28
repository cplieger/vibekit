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

/** Human-readable label for a forge kind (e.g. "GitHub", "Gitea / Forgejo"). */
export function forgeKindLabel(kind: ForgeKind): string {
  switch (kind) {
    case "github":
      return "GitHub";
    case "gitlab":
      return "GitLab";
    case "codeberg":
      return "Codeberg";
    case "gitea":
      return "Gitea / Forgejo";
  }
}

/** Centralized metadata for each forge kind. Single source of truth for
 *  display names and icon glyphs. */
export const FORGE_META: Record<
  ForgeKind,
  {
    title: string;
    icon: string;
  }
> = {
  github: {
    title: "GitHub",
    icon: '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>',
  },
  gitlab: {
    title: "GitLab",
    icon: '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 14.5l2.97-9.14H5.03L8 14.5z"/><path d="M8 14.5L5.03 5.36H1.18L8 14.5z" opacity=".7"/><path d="M1.18 5.36l-.95 2.93c-.09.27 0 .56.22.72L8 14.5 1.18 5.36z" opacity=".5"/><path d="M1.18 5.36h3.85L3.58.55c-.1-.3-.52-.3-.62 0L1.18 5.36z"/><path d="M8 14.5l2.97-9.14h3.85L8 14.5z" opacity=".7"/><path d="M14.82 5.36l.95 2.93c.09.27 0 .56-.22.72L8 14.5l6.82-9.14z" opacity=".5"/><path d="M14.82 5.36h-3.85l1.45-4.81c.1-.3.52-.3.62 0l1.78 4.81z"/></svg>',
  },
  codeberg: {
    title: "Codeberg",
    icon: '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C4.1 0 1 3.1 1 7c0 2.4 1.2 4.5 3 5.7V15l4-2.5L12 15v-2.3c1.8-1.2 3-3.3 3-5.7 0-3.9-3.1-7-7-7zm0 11c-2.2 0-4-1.8-4-4s1.8-4 4-4 4 1.8 4 4-1.8 4-4 4z"/></svg>',
  },
  gitea: {
    title: "Gitea / Forgejo",
    icon: '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1C4.13 1 1 4.13 1 8s3.13 7 7 7 7-3.13 7-7-3.13-7-7-7zm3.5 5.5c0 .28-.22.5-.5.5h-1v3c0 .55-.45 1-1 1H7c-.55 0-1-.45-1-1V7H5c-.28 0-.5-.22-.5-.5s.22-.5.5-.5h1V5c0-.55.45-1 1-1h2c.55 0 1 .45 1 1v1h1c.28 0 .5.22.5.5z"/></svg>',
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
