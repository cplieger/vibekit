// One owner for what a PAD is: a client-side reservation for a block whose own frame has
// not arrived. `store.ts` writes them and `turns.ts` reads the predicate, so both live in
// a leaf — following `step-subtask.ts`'s precedent, since importing the store from the
// pure projection would drag the whole store graph into it.
//
// `Block` comes from the generated wire module, matching the fleet's precedent for
// reading a wire type straight from it; `./types.js` re-exports the same symbol.

import type { Block } from "./wire/types.gen.js";

/** Spread helper: include `agent_subtask_id` only when non-empty, since
 *  exactOptionalPropertyTypes forbids setting an optional field to undefined.
 *
 *  `string | undefined` rather than `string`, because `ToolCall.agent_subtask_id` is
 *  OPTIONAL on the wire and two callers pass it straight through. An absent id stamps
 *  nothing, exactly as an empty one does, so no caller needs a per-site `?? ""`. */
export function subtaskField(id: string | undefined): { agent_subtask_id?: string } {
  return id !== undefined && id !== "" ? { agent_subtask_id: id } : {};
}

/** One pad, and BOTH of its fields are GUESSES.
 *
 *  The kind is `text` because that mounts a FILLABLE node where `thinking` would mount
 *  nothing. The subtask is inherited from whichever frame reached PAST this slot, not read
 *  off the frame that will fill it. `appendChunk`'s pad repair corrects both when the real
 *  delta lands, and the correction includes CLEARING an inherited id the real frame does
 *  not carry — a pad seated on the wrong delegate is the same misplacement as one seated
 *  on none. */
export function padBlock(subtaskID: string | undefined): Block {
  return { type: "text", ...subtaskField(subtaskID) };
}

/** Reserve the block indices below `upto` whose own frame has not arrived yet.
 *
 *  `block_index` is the server's position in ONE chronological array the client fills from
 *  TWO event streams, so a frame can legitimately name an index past the end of what has
 *  arrived. The pad reserves the DOM position, which is load-bearing rather than
 *  defensive: the block mounter is append-only, so a block whose frame lands late cannot
 *  be inserted between two mounted siblings.
 *
 *  `subtaskID` is the id of the frame that CAUSED the reservation, and stamping it is what
 *  keeps a delegate's or a workflow step's run of blocks whole. A pad is a placeholder for
 *  a block the client has not received, never a block the parent agent wrote — and
 *  `turns.ts` `isStepMessage` is a UNIVERSAL over the array, so an untagged pad among a
 *  step's blocks reads as the chat's own work and opens a headerless turn card. */
export function padBlocks(blocks: Block[], upto: number, subtaskID: string | undefined): void {
  while (blocks.length < upto) {
    blocks.push(padBlock(subtaskID));
  }
}

/** Whether `b` is still a pad: a kind, and nothing behind it. Every real block carries the
 *  content that created it, so this cannot misread one as a pad — and an inherited subtask
 *  id is not content, so a stamped pad is still recognised as one. */
export function isPadBlock(b: Block): boolean {
  return b.text === undefined && b.thinking === undefined && b.tool_call_id === undefined;
}
