// ---------------------------------------------------------------------------
// ONE delegate's last few output lines, projected out of the launching chat's
// messages. The transcript renders none of a delegate's blocks (`placeBlock` in
// `messages-blocks.ts` drops them), so the tail is derived rather than harvested
// from the DOM. Pure and DOM-free, so the caller owns how it reads the store.
// ---------------------------------------------------------------------------

import type { Block, Message, ToolCall } from "./types.js";
import { effect, touch } from "@cplieger/reactive";
import { get, messagesVersionOf } from "./store.js";
import { ensureBlockTextSig, ensureBlockThinkingSig } from "./store-signals.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";

/** How many trailing lines a card's tail shows. */
export const TAIL_LINES = 3;

/** One block's per-(message, block-index) streaming signal, for a caller that
 *  wants the delegate's prose to arrive in the tick its delta does. */
export interface TailSource {
  readonly messageID: string;
  readonly blockIndex: number;
  /** Whether the growth lands in `blockThinkingSigs` rather than `blockTextSigs`. */
  readonly thinking: boolean;
  /** The block's text as of this read, so a signal minted from it starts truthful. */
  readonly full: string;
}

export interface SubagentTail {
  /** The trailing lines, oldest first, at most `want` of them. */
  readonly lines: string[];
  /** The blocks the lines came FROM, which are exactly the blocks whose growth can
   *  change them: the walk stops at `want` lines, so nothing above them can reach. */
  readonly sources: TailSource[];
}

/** The last `want` non-empty lines of `s`, in order. Backwards from the end, so a
 *  40KB report costs its last three lines rather than a `split("\n")` over all of
 *  it on every delta. */
function tailOf(s: string, want: number): string[] {
  const out: string[] = [];
  let end = s.length;
  while (end > 0 && out.length < want) {
    const nl = s.lastIndexOf("\n", end - 1);
    const line = s
      .slice(nl + 1, end)
      .replace(/\s+/gu, " ")
      .trim();
    if (line !== "") {
      out.unshift(line);
    }
    end = nl;
  }
  return out;
}

/** The line a TOOL block contributes: its title, and nothing else. The card's claim
 *  line is `tool-card.ts`'s presentation of a call's input, so deriving it again
 *  here would be a second owner. Empty for the delegate's own INVOCATION, which the
 *  card's header names, and for a call the transcript renders nothing for. */
function toolLine(block: Block, tools: readonly ToolCall[]): string[] {
  const tc = tools.find((c) => c.id === block.tool_call_id);
  if (tc === undefined || isSubagentInvocation(tc) || isInternalToolTitle(tc.title)) {
    return [];
  }
  return tailOf(tc.title, 1);
}

/** Project one delegate's trailing output out of a conversation. Walks EVERY message
 *  backwards rather than stopping at the newest one holding the delegate, because a
 *  turn split by a mid-turn model switch puts one delegate's blocks in two assistant
 *  messages. An empty subtask id is nobody's delegate and answers nothing. */
export function subagentTail(
  messages: readonly Message[],
  subtaskID: string,
  want: number = TAIL_LINES,
): SubagentTail {
  const lines: string[] = [];
  const sources: TailSource[] = [];
  if (subtaskID === "" || want <= 0) {
    return { lines, sources };
  }
  for (let mi = messages.length - 1; mi >= 0 && lines.length < want; mi--) {
    const m = messages[mi];
    if (m === undefined) {
      continue;
    }
    const blocks = m.blocks ?? [];
    for (let i = blocks.length - 1; i >= 0 && lines.length < want; i--) {
      const block = blocks[i];
      if (block === undefined || (block.agent_subtask_id ?? "") !== subtaskID) {
        continue;
      }
      const thinking = block.type === "thinking";
      if (block.type === "tool_use") {
        lines.unshift(...toolLine(block, m.tool_calls ?? []));
        continue;
      }
      const full = (thinking ? block.thinking : block.text) ?? "";
      sources.unshift({ messageID: m.id, blockIndex: i, thinking, full });
      lines.unshift(...tailOf(full, want - lines.length));
    }
  }
  return { lines, sources };
}

/** Repaint ONE delegate's tail whenever its own output grows. Returns the disposer.
 *
 *  `ensure` rather than `get` on the block signals, unlike `subagent-view.ts`:
 *  nothing mounts a delegate's blocks, so minting the signal is what gives the store
 *  a sink to repaint through. The guard keeps a sibling's delta off this card — the
 *  walk re-runs, the paint does not. */
export function bindSubagentTail(
  chatID: string,
  subtaskID: string,
  paint: (lines: string[]) => void,
): () => void {
  let painted: string | undefined;
  return effect(() => {
    touch(messagesVersionOf(chatID));
    const { lines, sources } = subagentTail(get(chatID)?.messages ?? [], subtaskID);
    for (const s of sources) {
      touch(
        s.thinking
          ? ensureBlockThinkingSig(s.messageID, s.blockIndex, s.full)
          : ensureBlockTextSig(s.messageID, s.blockIndex, s.full),
      );
    }
    const next = lines.join("\n");
    if (next === painted) {
      return;
    }
    painted = next;
    paint(lines);
  });
}
