// Binary wire decoder. Mirrors wire_binary.go on the server.
//
// All multi-byte integers are little-endian. See wire_binary.go for the
// exact frame layout — wire message type tags and mode flags are generated
// from Go source into wire/constants.gen.ts by cmd/wire-codegen.

import type {
  WireRun,
  ScreenMessage,
  ScrollMessage,
  ResumeAckMessage,
  ModesMessage,
  ServerMessage,
} from "./term-types.js";

// Wire message type tags + mode flags (from vibecli's wire_binary.go).
const MSG_SCREEN = 0;
const MSG_SCROLL = 1;
const MSG_RESUME_ACK = 2;
const MSG_MODES = 3;
const MODE_FLAG_BRACKETED_PASTE = 1;
const MODE_FLAG_APP_CURSOR_KEYS = 2;

class Cursor {
  view: DataView;
  bytes: Uint8Array;
  off = 0;
  constructor(buf: ArrayBuffer) {
    this.view = new DataView(buf);
    this.bytes = new Uint8Array(buf);
  }
  u8(): number {
    const v = this.view.getUint8(this.off);
    this.off += 1;
    return v;
  }
  u16(): number {
    const v = this.view.getUint16(this.off, true);
    this.off += 2;
    return v;
  }
  i32(): number {
    const v = this.view.getInt32(this.off, true);
    this.off += 4;
    return v;
  }
  // Read a 64-bit unsigned integer using BigInt then narrow to Number.
  // Realistic byte counts fit in Number.MAX_SAFE_INTEGER (2^53), so the
  // narrow is lossless for our use; using Number throughout keeps the
  // arithmetic in connection.ts straightforward (no BigInt mixing).
  u64(): number {
    const v = this.view.getBigUint64(this.off, true);
    this.off += 8;
    return Number(v);
  }
  utf8(len: number): string {
    const slice = this.bytes.subarray(this.off, this.off + len);
    this.off += len;
    return new TextDecoder().decode(slice);
  }
}

function readRowRuns(c: Cursor): WireRun[] {
  const numRuns = c.u16();
  // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled immediately below
  const runs: WireRun[] = new Array(numRuns);
  for (let i = 0; i < numRuns; i++) {
    const tlen = c.u16();
    const t = c.utf8(tlen);
    const f = c.i32();
    const b = c.i32();
    const a = c.u16();
    const uc = c.i32();
    runs[i] = { t, f, b, a, uc };
  }
  return runs;
}

export function decodeWireBinary(buf: ArrayBuffer): ServerMessage | null {
  if (buf.byteLength < 9) {
    return null;
  } // 1B type + 8B ack
  try {
    return decodeWireBinaryInner(buf);
  } catch (err) {
    // Malformed frame: DataView throws RangeError on overrun. Drop the
    // frame rather than letting the error bubble into the WebSocket
    // message handler (where it would surface as an unhandled error
    // and clutter logs without stopping the message pump).
    if (err instanceof RangeError) {
      return null;
    }
    throw err;
  }
}

function decodeWireBinaryInner(buf: ArrayBuffer): ServerMessage | null {
  const c = new Cursor(buf);
  const msgType = c.u8();
  const inputAck = c.u64();

  if (msgType === MSG_RESUME_ACK) {
    // Optional 8-byte server epoch (boot-time nanoseconds) tail.
    // Older servers omit it (frame is exactly 9 bytes); newer servers
    // append it (17 bytes). Client uses the epoch to detect server
    // restart and reset session state — see connection.ts.
    let serverEpoch: number | undefined;
    if (buf.byteLength >= 17) {
      serverEpoch = c.u64();
    }
    const msg: ResumeAckMessage =
      serverEpoch !== undefined
        ? { type: "resumeAck", received: inputAck, serverEpoch }
        : { type: "resumeAck", received: inputAck };
    return msg;
  }
  if (msgType === MSG_SCREEN) {
    const cursorRow = c.u16();
    const cursorCol = c.u16();
    const screenHeight = c.u16();
    const numChanged = c.u16();
    const cursorStyle = c.u8();
    const cursorFlags = c.u8();
    const cursorHidden = (cursorFlags & 1) !== 0;
    const bell = (cursorFlags & 2) !== 0;
    const cursorBlink = (cursorFlags & 4) !== 0;
    // The screen message carries only the changed rows alongside their
    // indices; absent rows are kept as undefined so the renderer leaves
    // them untouched. rows.length matches the screen height so
    // ensureRows() in the renderer creates the right number of <div>s.
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const rows: WireRun[][] = new Array(screenHeight);
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const changed: number[] = new Array(numChanged);
    for (let i = 0; i < numChanged; i++) {
      const idx = c.u16();
      changed[i] = idx;
      rows[idx] = readRowRuns(c);
    }
    const msg: ScreenMessage = {
      type: "screen",
      rows,
      cursor: [cursorRow, cursorCol],
      changed,
      cursorStyle,
      cursorHidden,
      cursorBlink,
      bell,
      inputAck,
    };
    return msg;
  }
  if (msgType === MSG_SCROLL) {
    const numLines = c.u16();
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const lines: WireRun[][] = new Array(numLines);
    for (let i = 0; i < numLines; i++) {
      lines[i] = readRowRuns(c);
    }
    const msg: ScrollMessage = { type: "scroll", lines, inputAck };
    return msg;
  }
  if (msgType === MSG_MODES) {
    const flags = c.u8();
    const msg: ModesMessage = {
      type: "modes",
      bracketedPaste: (flags & MODE_FLAG_BRACKETED_PASTE) !== 0,
      applicationCursor: (flags & MODE_FLAG_APP_CURSOR_KEYS) !== 0,
      inputAck,
    };
    return msg;
  }
  return null;
}
