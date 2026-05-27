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
