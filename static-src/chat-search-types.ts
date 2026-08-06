// Cross-chat search wire types, hand-declared beside the feature (the
// forge-types.ts precedent): one endpoint, one record.

/** One hit inside a chat; mirrors internal/chat.SearchHit. */
interface ChatSearchHit {
  message_id: string;
  turn_message_id: string;
  excerpt: string;
  role: string;
  turn: number;
  offset: number;
}

/** One matching chat; mirrors internal/chat.Match. */
export interface ChatSearchMatch {
  id: string;
  name: string;
  /** The best line to show. A TITLE-only match has no excerpt. */
  best: ChatSearchHit;
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
