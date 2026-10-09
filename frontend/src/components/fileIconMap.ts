import {
  Container,
  Database,
  File,
  FileArchive,
  FileBraces,
  FileCode,
  FileCog,
  FileImage,
  FileLock,
  FileMusic,
  FileSpreadsheet,
  FileTerminal,
  FileText,
  FileVideoCamera,
  Folder,
  FolderOpen,
  GitBranch,
  Package,
  Presentation,
} from "lucide-vue-next";
import type { Component } from "vue";

/**
 * File icons are monochrome lucide glyphs rather than per-language colored
 * badges: `index.css` states the palette is deliberately achromatic, and a
 * grid of saturated language logos ends up the loudest thing on the page.
 *
 * The trade-off is that every language collapses onto `code` — the file name
 * beside the icon is what tells `main.go` from `main.rs`. Icons only need to
 * separate the coarse categories a person actually scans for: directory,
 * runnable script, image, archive, config.
 */
export type IconKey =
  | "folder"
  | "folder-open"
  | "code"
  | "json"
  | "config"
  | "text"
  | "terminal"
  | "database"
  | "sheet"
  | "presentation"
  | "image"
  | "video"
  | "audio"
  | "archive"
  | "container"
  | "git"
  | "package"
  | "lock"
  | "file";

/** Icon key → lucide component. Consumers render `ICONS[resolveIcon(...)]`. */
export const ICONS: Record<IconKey, Component> = {
  folder: Folder,
  "folder-open": FolderOpen,
  code: FileCode,
  json: FileBraces,
  config: FileCog,
  text: FileText,
  terminal: FileTerminal,
  database: Database,
  sheet: FileSpreadsheet,
  presentation: Presentation,
  image: FileImage,
  video: FileVideoCamera,
  audio: FileMusic,
  archive: FileArchive,
  container: Container,
  git: GitBranch,
  package: Package,
  lock: FileLock,
  file: File,
};

/** Maps file extensions to icon keys. */
const extToIcon: Record<string, IconKey> = {
  // Source — every language collapses onto one glyph by design.
  js: "code",
  mjs: "code",
  cjs: "code",
  jsx: "code",
  ts: "code",
  mts: "code",
  cts: "code",
  tsx: "code",
  vue: "code",
  svelte: "code",
  go: "code",
  mod: "code",
  sum: "code",
  py: "code",
  pyw: "code",
  pyx: "code",
  rs: "code",
  java: "code",
  jar: "code",
  kt: "code",
  kts: "code",
  c: "code",
  h: "code",
  cpp: "code",
  cxx: "code",
  cc: "code",
  hpp: "code",
  hxx: "code",
  rb: "code",
  php: "code",
  lua: "code",
  swift: "code",
  dart: "code",
  asm: "code",
  s: "code",
  html: "code",
  htm: "code",
  xml: "code",
  css: "code",
  scss: "code",
  sass: "code",
  less: "code",
  graphql: "code",
  gql: "code",
  proto: "code",

  json: "json",
  json5: "json",
  jsonl: "json",

  yaml: "config",
  yml: "config",
  toml: "config",
  ini: "config",
  conf: "config",
  env: "config",

  // `pdf`/`doc` land here too: lucide's `FileType` is the *font-file* glyph
  // (it draws a "T"), so a document is closer to plain text than to that.
  md: "text",
  markdown: "text",
  txt: "text",
  log: "text",
  rst: "text",
  pdf: "text",
  doc: "text",
  docx: "text",

  sh: "terminal",
  bash: "terminal",
  zsh: "terminal",
  fish: "terminal",

  sql: "database",
  db: "database",
  sqlite: "database",

  csv: "sheet",
  tsv: "sheet",
  xls: "sheet",
  xlsx: "sheet",

  ppt: "presentation",
  pptx: "presentation",
  odp: "presentation",
  key: "presentation",

  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  bmp: "image",
  ico: "image",
  svg: "image",
  tiff: "image",

  mp4: "video",
  webm: "video",
  mov: "video",
  avi: "video",
  mkv: "video",

  mp3: "audio",
  wav: "audio",
  ogg: "audio",
  flac: "audio",
  aac: "audio",
  m4a: "audio",

  zip: "archive",
  tar: "archive",
  gz: "archive",
  bz2: "archive",
  "7z": "archive",
  rar: "archive",
  xz: "archive",
  zst: "archive",

  lock: "lock",
  dockerfile: "container",
};

/** Maps special filenames (lowercase) to icon keys. */
const nameToIcon: Record<string, IconKey> = {
  dockerfile: "container",
  "docker-compose.yml": "container",
  "docker-compose.yaml": "container",
  "compose.yml": "container",
  "compose.yaml": "container",
  makefile: "config",
  ".gitignore": "git",
  ".gitattributes": "git",
  ".gitmodules": "git",
  "package.json": "package",
  "go.mod": "code",
  "go.sum": "code",
  "package-lock.json": "lock",
  "pnpm-lock.yaml": "lock",
  "yarn.lock": "lock",
  "cargo.lock": "lock",
  "uv.lock": "lock",
};

/**
 * Resolve the icon key for a file/folder entry.
 *
 * `expanded` only applies to directories, so a tree can show the open-folder
 * variant on the row it just expanded.
 */
export function resolveIcon(fileName: string, isDir: boolean, expanded = false): IconKey {
  if (isDir) return expanded ? "folder-open" : "folder";

  const lower = fileName.toLowerCase();

  // Check special filenames first
  const byName = nameToIcon[lower];
  if (byName) return byName;

  // Extract extension
  const dotIdx = lower.lastIndexOf(".");
  const ext = dotIdx >= 0 ? lower.slice(dotIdx + 1) : "";

  return extToIcon[ext] ?? "file";
}
