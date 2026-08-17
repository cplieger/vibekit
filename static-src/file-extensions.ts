// ---------------------------------------------------------------------------
// Single source-of-truth for recognized file extensions. Consumed by icons.ts
// (icon key), linkify.ts (path regex), and highlight.ts (language detection).
// ---------------------------------------------------------------------------

/** Per-extension metadata. All fields optional — absence means "not applicable". */
export interface ExtMeta {
  /** Internal language key for syntax highlighting (e.g. "go", "ts", "py"). */
  lang?: string;
  /** Icon key into FILE_ICONS (e.g. "go", "ts", "config"). */
  iconKey?: string;
  /** Emoji badge for attachment pills (e.g. "📄", "🖼️"). */
  badge?: string;
}

/**
 * Canonical extension registry. Every recognized source/config file extension
 * appears here exactly once. Add new extensions in one place.
 */
export const KNOWN_EXTENSIONS: Readonly<Record<string, ExtMeta>> = {
  // TypeScript / JavaScript
  ts: { lang: "ts", iconKey: "ts" },
  tsx: { lang: "ts", iconKey: "ts" },
  js: { lang: "js", iconKey: "js" },
  jsx: { lang: "js", iconKey: "js" },
  mjs: { lang: "js", iconKey: "js" },
  cjs: { lang: "js", iconKey: "js" },
  // Go
  go: { lang: "go", iconKey: "go" },
  mod: { lang: "go", iconKey: "go" },
  sum: { iconKey: "go" },
  // Python
  py: { lang: "py", iconKey: "py" },
  // Rust
  rs: { lang: "rs", iconKey: "rs" },
  // Ruby
  rb: { lang: "rb", iconKey: "ruby" },
  // PHP
  php: { lang: "php", iconKey: "php" },
  // Java / Kotlin
  java: { lang: "java", iconKey: "java" },
  kt: { iconKey: "java" },
  // C / C++
  c: { lang: "c", iconKey: "c" },
  h: { lang: "c", iconKey: "c" },
  cpp: { lang: "c", iconKey: "c" },
  cc: { lang: "c", iconKey: "c" },
  hpp: { lang: "c", iconKey: "c" },
  cs: { iconKey: "c" },
  // Shell
  sh: { lang: "sh", iconKey: "sh" },
  bash: { lang: "sh", iconKey: "sh" },
  zsh: { lang: "sh", iconKey: "sh" },
  // Data / Config
  json: { lang: "json", iconKey: "json" },
  yaml: { lang: "yaml", iconKey: "yaml" },
  yml: { lang: "yaml", iconKey: "yaml" },
  toml: { lang: "toml", iconKey: "yaml" },
  xml: { lang: "xml", iconKey: "xml" },
  svg: { iconKey: "xml" },
  ini: { iconKey: "config" },
  env: { iconKey: "lock" },
  conf: { iconKey: "config" },
  cfg: { iconKey: "config" },
  // Markup
  md: { lang: "md", iconKey: "md" },
  mdx: { lang: "md", iconKey: "md" },
  html: { lang: "html", iconKey: "html" },
  htm: { lang: "html", iconKey: "html" },
  // CSS
  css: { lang: "css", iconKey: "css" },
  scss: { iconKey: "sass" },
  sass: { iconKey: "sass" },
  less: { iconKey: "less" },
  // Frameworks
  vue: { iconKey: "vue" },
  svelte: { iconKey: "html" },
  // SQL / Query
  sql: { lang: "sql", iconKey: "config" },
  graphql: {},
  proto: {},
  // Text / Logs
  txt: { iconKey: "file" },
  rst: {},
  log: { iconKey: "file" },
  lock: {},
  tmp: {},
  // Images
  png: { iconKey: "img" },
  jpg: { iconKey: "img" },
  jpeg: { iconKey: "img" },
  gif: { iconKey: "img" },
  webp: { iconKey: "img" },
  ico: { iconKey: "img" },
  bmp: { iconKey: "img" },
  // Docker (pseudo-extension for fenced-code detection)
  dockerfile: { lang: "docker" },
  // Lua
  lua: { iconKey: "lua" },
};

/** Flat array of all recognized extensions (for regex construction). */
export const FILE_EXTS: readonly string[] = Object.keys(KNOWN_EXTENSIONS);

/** Look up the language key for an extension. Returns "" if unknown. */
export function extToLang(ext: string): string {
  return KNOWN_EXTENSIONS[ext]?.lang ?? "";
}

/** Look up the icon key for an extension. Returns "" if unknown. */
export function extToIconKey(ext: string): string {
  return KNOWN_EXTENSIONS[ext]?.iconKey ?? "";
}

/** Badge-only extension registry for attachment pills. Extensions not in
 *  KNOWN_EXTENSIONS (binary formats) live here. The lookup function
 *  checks both registries so attachments.ts has a single source of truth. */
const BADGE_ONLY: Readonly<Record<string, string>> = {
  pdf: "📄",
  doc: "📝",
  docx: "📝",
  xls: "📊",
  xlsx: "📊",
  csv: "📊",
  ppt: "📽️",
  pptx: "📽️",
  png: "🖼️",
  jpg: "🖼️",
  jpeg: "🖼️",
  gif: "🖼️",
  svg: "🖼️",
  webp: "🖼️",
  zip: "📦",
  tar: "📦",
  gz: "📦",
  json: "{ }",
  yaml: "{ }",
  yml: "{ }",
  toml: "{ }",
};

/** Look up the emoji badge for an extension (without leading dot).
 *  Returns "" if no badge is defined. */
export function badgeForExt(ext: string): string {
  return KNOWN_EXTENSIONS[ext]?.badge ?? BADGE_ONLY[ext] ?? "";
}

/** The lowercased extension of a path or filename, without the dot. "" when
 *  there is none — including for a dotfile, whose leading dot names the file
 *  rather than typing it. Only looks after the last separator, so a dot in a
 *  directory name cannot be mistaken for an extension. */
export function extOf(path: string): string {
  const slash = path.lastIndexOf("/");
  const dot = path.lastIndexOf(".");
  if (dot <= slash + 1) {
    return "";
  }
  return path.slice(dot + 1).toLowerCase();
}

/** Extensions a browser renders as an image.
 *
 *  This is the BROWSER's question, and it has exactly one answer, so both
 *  browser-side consumers read it: the editor's image surface and the markdown
 *  `<img>` rewrite (which carried its own regex). It deliberately does NOT match
 *  `internal/command/prompt_attachments.go`'s `imageExts`, whose comment says so
 *  explicitly — that list is what KAS accepts as an inline image content block,
 *  a different consumer with a different answer.
 *
 *  `.svg` is in the set because an SVG referenced AS AN IMAGE is inert by
 *  specification: it may not fetch resources, run script, or reach the
 *  embedding document. That property belongs to `<img>` alone. A same-origin
 *  navigation to the same file executes its script, and so does an `<iframe>`
 *  pointing at it — `object-src 'none'` kills `<object>`/`<embed>`, but
 *  `frame-src` falls back to `default-src 'self'`, which PERMITS a same-origin
 *  frame. So a consumer of this predicate must RENDER the file and must never
 *  offer it as a link or a frame on vibekit's own origin. */
const VIEWABLE_IMAGE_EXTS: ReadonlySet<string> = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "avif",
  "ico",
  "bmp",
]);

/** Whether a path names an image the browser can paint in an `<img>`. */
export function isViewableImage(path: string): boolean {
  return VIEWABLE_IMAGE_EXTS.has(extOf(path));
}

/** Extensions `<audio controls>` can play.
 *
 *  Native element, no dependency: the browser already ships a player with
 *  transport controls, a timeline and keyboard support, and it degrades to its
 *  own fallback content when the codec is missing. */
const PLAYABLE_AUDIO_EXTS: ReadonlySet<string> = new Set([
  "mp3",
  "wav",
  "ogg",
  "m4a",
  "flac",
  "aac",
  "opus",
]);

/** Whether a path names audio the browser can play in an `<audio>` element. */
export function isPlayableAudio(path: string): boolean {
  return PLAYABLE_AUDIO_EXTS.has(extOf(path));
}
