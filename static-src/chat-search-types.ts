// Cross-chat search wire types, hand-declared beside the feature (the
// forge-types.ts precedent): one endpoint, one record.

import type { SearchHit } from "./chat-search.js";

/** One matching chat; mirrors internal/chat.Match. */
export interface ChatSearchMatch {
  id: string;
  name: string;
  /** The best line to show — the same wire type per-chat search returns
   *  (internal/chat.SearchHit), so the ONE fixture-pinned mirror serves both
   *  endpoints and a second partial copy cannot drift. A TITLE-only match has
   *  no excerpt. */
  best: SearchHit;
  hits: number;
  score: number;
  updated_at: number;
}

/** Mirrors internal/chat.SearchAllResult. */
export interface ChatSearchResult {
  matches: ChatSearchMatch[];
  scanned: number;
  /** True when the scan hit its file cap, so older chats were not read. The UI
   *  must say so rather than let an empty result imply "not there". */
  truncated: boolean;
}
