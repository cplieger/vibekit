// Current DEC private mode state, as last announced by the server via
// a wireMsgModes frame. Used by:
//   - composition.ts onPaste: wraps in \e[200~..\e[201~ only when
//     bracketedPaste is on; otherwise sends raw text. Same for the
//     Ctrl+Shift+V handler in app.ts.
//   - keyboard.ts arrow-key encoder: emits SS3 (ESC O letter) when
//     applicationCursor is true, CSI (ESC [ letter) otherwise.
//
// Defaults match the conservative "always-on" assumption the client
// used before PROTO-01 — so until the first modes frame arrives,
// behavior is unchanged. The server prime the client with a baseline
// modes frame on the first flush, so the steady state is the server's
// truth, not these defaults.

let bracketedPaste = true;
let applicationCursor = false;

export function setModes(bracketed: boolean, appCursor: boolean): void {
  bracketedPaste = bracketed;
  applicationCursor = appCursor;
}

export function isBracketedPaste(): boolean {
  return bracketedPaste;
}

export function isApplicationCursor(): boolean {
  return applicationCursor;
}
