// ---------------------------------------------------------------------------
// Ambient declarations for the xterm.js vendor modules. The modules are
// served at /vendor/xterm/*.mjs in production (placed there by the
// Dockerfile) and resolved from bare specifiers via the <script
// type="importmap"> in index.html. The import map hash is included in
// the CSP script-src directive (security.go).
//
// These files aren't present at type-check time (they're added to the
// image during `docker build`), so we declare only the public API surface
// shell.ts actually touches. Keep this narrow; if more is needed, copy
// from @xterm/xterm's shipped typings/xterm.d.ts.
// ---------------------------------------------------------------------------

declare module "xterm" {
  export interface IDisposable {
    dispose(): void;
  }
  export type IEvent<T> = (listener: (arg: T) => void) => IDisposable;

  export interface ITheme {
    foreground?: string;
    background?: string;
    cursor?: string;
    cursorAccent?: string;
    selectionBackground?: string;
    selectionInactiveBackground?: string;
    black?: string;
    red?: string;
    green?: string;
    yellow?: string;
    blue?: string;
    magenta?: string;
    cyan?: string;
    white?: string;
    brightBlack?: string;
    brightRed?: string;
    brightGreen?: string;
    brightYellow?: string;
    brightBlue?: string;
    brightMagenta?: string;
    brightCyan?: string;
    brightWhite?: string;
  }

  export interface ITerminalOptions {
    allowProposedApi?: boolean;
    cursorBlink?: boolean;
    cursorStyle?: "block" | "bar" | "underline";
    cursorInactiveStyle?: "outline" | "block" | "bar" | "underline" | "none";
    fontFamily?: string;
    fontSize?: number;
    macOptionIsMeta?: boolean;
    minimumContrastRatio?: number;
    rightClickSelectsWord?: boolean;
    scrollback?: number;
    scrollOnUserInput?: boolean;
    smoothScrollDuration?: number;
    theme?: ITheme;
  }

  export interface ITerminalAddon extends IDisposable {
    activate(terminal: Terminal): void;
  }

  export class Terminal implements IDisposable {
    readonly cols: number;
    readonly rows: number;
    options: ITerminalOptions;
    constructor(options?: ITerminalOptions);
    open(parent: HTMLElement): void;
    focus(): void;
    clear(): void;
    write(data: string | Uint8Array): void;
    loadAddon(addon: ITerminalAddon): void;
    onData: IEvent<string>;
    onBinary: IEvent<string>;
    onResize: IEvent<{ cols: number; rows: number }>;
    onTitleChange: IEvent<string>;
    dispose(): void;
  }
}

declare module "xterm/addon-fit" {
  import type { ITerminalAddon, Terminal } from "xterm";
  export class FitAddon implements ITerminalAddon {
    activate(terminal: Terminal): void;
    dispose(): void;
    fit(): void;
    proposeDimensions(): { cols: number; rows: number } | undefined;
  }
}

declare module "xterm/addon-webgl" {
  import type { ITerminalAddon, Terminal } from "xterm";
  export class WebglAddon implements ITerminalAddon {
    constructor(preserveDrawingBuffer?: boolean);
    activate(terminal: Terminal): void;
    dispose(): void;
  }
}

declare module "xterm/addon-web-links" {
  import type { ITerminalAddon, Terminal } from "xterm";
  export class WebLinksAddon implements ITerminalAddon {
    constructor(handler?: (event: MouseEvent, uri: string) => void);
    activate(terminal: Terminal): void;
    dispose(): void;
  }
}
