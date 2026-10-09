import { apiBase } from "./apiBase";
import { ApiError, jsonRequestInit, request } from "./apiClient";
import { xhrUpload, type XhrUploadOptions } from "./xhrUpload";
import { friendlyUploadErrorMessage } from "@/lib/uploadHelpers";

export type { UploadProgress } from "./xhrUpload";

export interface UploadOptions extends XhrUploadOptions {
  // When the upload would land on an existing path, "overwrite" truncates
  // the existing file, "rename" picks a free name via the backend's
  // dedupName helper, and omitting the field (or passing "error") makes
  // the backend reply 409 so the UI can prompt.
  onConflict?: "overwrite" | "rename";
}

export class UploadConflictError extends Error {
  status = 409 as const;
  conflicts: string[];
  constructor(conflicts: string[]) {
    super("upload: conflict");
    this.conflicts = conflicts;
  }
}

// Thrown when a save loses the optimistic-lock race — the file changed on
// disk (almost always an agent rewriting it) after the editor read it. Carries
// the current validator so the caller can re-sync without another round-trip.
export class WriteConflictError extends Error {
  status = 412 as const;
  etag: string;
  constructor(etag: string) {
    super("write file: conflict");
    this.etag = etag;
  }
}

export interface FileEntry {
  name: string;
  is_dir: boolean;
  size: number;
  modified: string;
}

export interface FileInspection {
  path: string;
  name: string;
  is_dir: boolean;
  size: number;
  modified: string;
  content_type: string;
  is_text: boolean;
}

export interface WriteResult {
  path: string;
  size: number;
  modified: string;
  etag: string;
}

export type SearchMode = "name" | "content";

export interface SearchMatch {
  line: number;
  text: string;
}

export interface SearchResult {
  path: string;
  name: string;
  size: number;
  modified: string;
  matches: SearchMatch[];
}

export interface SearchResponse {
  query: string;
  mode: SearchMode;
  truncated: boolean;
  results: SearchResult[];
}

export interface DirListing {
  path: string;
  entries: FileEntry[];
}

export function useFileApi() {
  async function listDir(userId: string, path: string): Promise<DirListing> {
    const params = new URLSearchParams({ path });
    return request<DirListing>(
      `/api/users/${encodeURIComponent(userId)}/files?${params}`,
      undefined,
      { label: "list dir" },
    );
  }

  async function readFile(userId: string, path: string): Promise<Response> {
    const params = new URLSearchParams({ path });
    return request<Response>(
      `/api/users/${encodeURIComponent(userId)}/files/read?${params}`,
      undefined,
      { label: "read file", expect: "response" },
    );
  }

  async function inspectFile(userId: string, path: string): Promise<FileInspection> {
    const params = new URLSearchParams({ path });
    return request<FileInspection>(
      `/api/users/${encodeURIComponent(userId)}/files/inspect?${params}`,
      undefined,
      { label: "inspect file" },
    );
  }

  // writeFile overwrites (or creates) the text file at `path` with `content`.
  // Surfaces the backend's structured error message on failure so the editor
  // can show "path is a directory" / "file too large" / etc. instead of a
  // bare HTTP status.
  //
  // Passing the `etag` the file was read at turns the write into a
  // compare-and-swap: if an agent rewrote the file in the meantime the server
  // answers 412 and this throws WriteConflictError instead of clobbering it.
  // Omitting it keeps the old last-write-wins behaviour, which is what the
  // "overwrite anyway" path deliberately asks for.
  async function writeFile(
    userId: string,
    path: string,
    content: string,
    etag?: string,
  ): Promise<WriteResult> {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (etag) headers["If-Match"] = etag;
    try {
      return await request<WriteResult>(
        `/api/users/${encodeURIComponent(userId)}/files/write`,
        { method: "PUT", headers, body: JSON.stringify({ path, content }) },
        { label: "write file", errorBody: "suffix" },
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        const etag = (err.body as { etag?: string } | undefined)?.etag;
        throw new WriteConflictError(etag || "");
      }
      throw err;
    }
  }

  // searchFiles walks the user's workspace. `name` mode matches the query
  // against each file's workdir-relative path (what a Cmd+P picker wants),
  // `content` mode greps line-wise through text files.
  async function searchFiles(
    userId: string,
    options: { query: string; mode?: SearchMode; path?: string; limit?: number },
    signal?: AbortSignal,
  ): Promise<SearchResponse> {
    const params = new URLSearchParams({ q: options.query });
    if (options.mode) params.set("mode", options.mode);
    if (options.path) params.set("path", options.path);
    if (options.limit) params.set("limit", String(options.limit));
    return request<SearchResponse>(
      `/api/users/${encodeURIComponent(userId)}/files/search?${params}`,
      { signal },
      { label: "search files" },
    );
  }

  function downloadFileUrl(userId: string, path: string): string {
    const params = new URLSearchParams({ path });
    return `${apiBase()}/api/users/${encodeURIComponent(userId)}/files/download?${params}`;
  }

  // Inline-serving URL — uses the read endpoint which sets the right Content-Type
  // and (unlike downloadFileUrl) does NOT send Content-Disposition: attachment,
  // so iframes/embeds can render binary types like PDF inline instead of being
  // hijacked into a download.
  function readFileUrl(userId: string, path: string): string {
    const params = new URLSearchParams({ path });
    return `${apiBase()}/api/users/${encodeURIComponent(userId)}/files/read?${params}`;
  }

  // Rendered-page URL — the file's workspace path becomes the URL's path, so
  // `./app.js` inside a previewed page resolves to the file sitting next to it
  // instead of to /api/users/…/files/. Everything the page pulls in (scripts,
  // wasm, images, files in subdirectories) travels through this same endpoint.
  function previewFileUrl(userId: string, path: string): string {
    return `${previewUrlPrefix(userId)}${encodePreviewPath(path)}`;
  }

  function downloadZipUrl(userId: string, path: string | string[]): string {
    const params = new URLSearchParams();
    for (const p of Array.isArray(path) ? path : [path]) {
      params.append("path", p);
    }
    return `${apiBase()}/api/users/${encodeURIComponent(userId)}/files/download-zip?${params}`;
  }

  async function uploadFiles(
    userId: string,
    path: string,
    files: File[],
    options?: UploadOptions,
  ): Promise<void> {
    if (!options?.onConflict) {
      const paths = files.map((file) => file.webkitRelativePath || file.name);
      const check = await request<{ conflicts: string[] }>(
        `/api/users/${encodeURIComponent(userId)}/files/upload/check`,
        { ...jsonRequestInit("POST", { path, paths }), signal: options?.signal },
        { label: "check upload conflicts" },
      );
      if (check.conflicts.length > 0) throw new UploadConflictError(check.conflicts);
    }
    const form = new FormData();
    for (const f of files) {
      form.append("files", f);
      form.append("paths", f.webkitRelativePath || f.name);
    }
    if (options?.onConflict) {
      form.append("on_conflict", options.onConflict);
    }
    const params = new URLSearchParams({ path });
    const url = `${apiBase()}/api/users/${encodeURIComponent(userId)}/files/upload?${params}`;

    const xhr = await xhrUpload(url, form, options);
    if (xhr.status >= 200 && xhr.status < 300) return;
    if (xhr.status === 409) {
      let conflicts: string[] = [];
      try {
        const body = JSON.parse(xhr.responseText) as { conflicts?: string[] };
        if (Array.isArray(body.conflicts)) conflicts = body.conflicts;
      } catch {
        // fall through with empty conflicts list
      }
      throw new UploadConflictError(conflicts);
    }
    // 413 most often comes from a reverse proxy (Nginx
    // `client_max_body_size`) rather than DayMug itself. Surface a
    // localized hint that names the proxy so the user knows to ask
    // their admin to raise the limit instead of trying again.
    throw new Error(friendlyUploadErrorMessage(xhr.status) ?? `upload: ${xhr.status}`);
  }

  async function deleteFile(userId: string, path: string): Promise<void> {
    const params = new URLSearchParams({ path });
    await request<void>(
      `/api/users/${encodeURIComponent(userId)}/files?${params}`,
      { method: "DELETE" },
      { label: "delete", expect: "none" },
    );
  }

  async function renameFile(userId: string, oldPath: string, newPath: string): Promise<void> {
    await request<void>(
      `/api/users/${encodeURIComponent(userId)}/files/rename`,
      jsonRequestInit("PUT", { old_path: oldPath, new_path: newPath }),
      { label: "rename", expect: "none" },
    );
  }

  async function mkdir(userId: string, path: string): Promise<void> {
    await request<void>(
      `/api/users/${encodeURIComponent(userId)}/files/mkdir`,
      jsonRequestInit("POST", { path }),
      { label: "mkdir", expect: "none" },
    );
  }

  async function moveFile(
    userId: string,
    srcPath: string,
    dstPath: string,
    onConflict?: "overwrite" | "rename",
  ): Promise<void> {
    const payload: Record<string, string> = { src_path: srcPath, dst_path: dstPath };
    if (onConflict) payload.on_conflict = onConflict;
    // The thrown ApiError carries the status; a 409 lets the caller offer
    // overwrite / rename.
    await request<void>(
      `/api/users/${encodeURIComponent(userId)}/files/move`,
      jsonRequestInit("PUT", payload),
      { label: "move", expect: "none" },
    );
  }

  async function copyFile(
    userId: string,
    srcPath: string,
    dstPath: string,
  ): Promise<{ path: string }> {
    return request<{ path: string }>(
      `/api/users/${encodeURIComponent(userId)}/files/copy`,
      jsonRequestInit("POST", { src_path: srcPath, dst_path: dstPath }),
      { label: "copy" },
    );
  }

  // Extracts an open-format archive (.zip/.tar/.tar.gz/.tar.bz2) into the
  // archive's parent directory. When the archive already wraps its contents
  // in a single top-level entry — the common case after Compress, which
  // prefixes every entry with the source basename — that wrapper is promoted
  // directly so foo.zip round-trips back to foo/ rather than foo/foo/.
  // Otherwise the loose contents are placed under a fresh dir named after
  // the archive stem. Collisions on either name are deduped. Returns the
  // relative path of the promoted entry so callers can highlight it.
  async function extractArchive(
    userId: string,
    path: string,
  ): Promise<{ path: string; extracted: number }> {
    // The unsupported-format and zip-slip rejections both arrive as 400 with
    // a useful `error` field; a bare status string would mask them.
    return request<{ path: string; extracted: number }>(
      `/api/users/${encodeURIComponent(userId)}/files/extract`,
      jsonRequestInit("POST", { path }),
      { label: "extract", errorBody: "suffix" },
    );
  }

  // Packs a multi-selection (files and/or folders) into a single .zip in the
  // given target directory. Default name is the source's basename when only
  // one path was selected, "Archive.zip" otherwise; backend dedups if the
  // target already exists. Returns the relative path of the new archive so
  // the caller can refresh the listing and highlight it.
  async function compressToZip(
    userId: string,
    paths: string[],
    targetDir: string,
    name?: string,
  ): Promise<{ path: string; name: string }> {
    return request<{ path: string; name: string }>(
      `/api/users/${encodeURIComponent(userId)}/files/compress`,
      jsonRequestInit("POST", { paths, target_dir: targetDir, name: name ?? "" }),
      { label: "compress", errorBody: "suffix" },
    );
  }

  return {
    listDir,
    inspectFile,
    readFile,
    writeFile,
    searchFiles,
    downloadFileUrl,
    readFileUrl,
    previewFileUrl,
    downloadZipUrl,
    uploadFiles,
    deleteFile,
    renameFile,
    mkdir,
    moveFile,
    copyFile,
    extractArchive,
    compressToZip,
  };
}

// ── Rendered-page preview URLs ─────────────────────────────────────
// The preview endpoint carries the workspace path *as* the URL path (see
// previewFileUrl). These two are inverses of each other: the component that
// frames a page builds URLs with one, and maps the resources the page ended up
// loading back to workspace paths — so they can be watched for changes — with
// the other.

export function previewUrlPrefix(userId: string): string {
  return `${apiBase()}/api/users/${encodeURIComponent(userId)}/files/preview/`;
}

// Per segment, so the slashes stay slashes and everything else (spaces, #, ?,
// non-ASCII) survives the round trip.
function encodePreviewPath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

// Returns null for anything that isn't a preview URL for this user — a page is
// free to load from a CDN, and those resources have no workspace path.
export function workspacePathFromPreviewUrl(userId: string, url: string): string | null {
  const prefix = previewUrlPrefix(userId);
  if (!url.startsWith(prefix)) return null;
  const rest = url.slice(prefix.length).split(/[?#]/)[0];
  if (!rest) return null;
  try {
    return rest.split("/").map(decodeURIComponent).join("/");
  } catch {
    // Malformed percent-encoding: not a path we can name to the watcher.
    return null;
  }
}

const htmlExts = new Set([".html", ".htm"]);

// isHtmlFile marks the files the workbench opens as a rendered page by
// default. Kept here next to isEditableText so the preview surface and the
// components deciding which view to show read the same definition.
export function isHtmlFile(name: string): boolean {
  const base = name.toLowerCase().split("/").pop() ?? "";
  const dot = base.lastIndexOf(".");
  return dot >= 0 && htmlExts.has(base.slice(dot));
}

// isEditableText decides whether a path is reasonable to open in the
// in-browser editor. Mirrors the text/code/markdown detection in
// FilePreview.vue so the "Edit" affordances only surface for files Monaco
// can actually render usefully — opening a 200 MB binary in a text editor
// is a footgun even with the backend's size cap.
const editableTextExts = new Set([
  ".txt",
  ".log",
  ".csv",
  ".env",
  ".gitignore",
  ".editorconfig",
  ".md",
  ".markdown",
  ".json",
  ".js",
  ".ts",
  ".jsx",
  ".tsx",
  ".vue",
  ".go",
  ".py",
  ".rb",
  ".rs",
  ".java",
  ".c",
  ".cpp",
  ".h",
  ".hpp",
  ".css",
  ".scss",
  ".less",
  ".html",
  ".xml",
  ".yaml",
  ".yml",
  ".toml",
  ".ini",
  ".sh",
  ".bash",
  ".zsh",
  ".fish",
  ".sql",
  ".graphql",
  ".proto",
  ".dockerfile",
  ".makefile",
  ".conf",
  ".cfg",
  ".properties",
  ".lock",
  ".npmrc",
  ".prettierrc",
  ".eslintrc",
  ".dockerignore",
  ".gitattributes",
]);
const editableTextBasenames = new Set(["makefile", "dockerfile", "go.mod", "go.sum"]);
const knownNonTextExts = new Set([
  ".png",
  ".jpg",
  ".jpeg",
  ".gif",
  ".webp",
  ".svg",
  ".bmp",
  ".ico",
  ".mp3",
  ".wav",
  ".ogg",
  ".flac",
  ".aac",
  ".m4a",
  ".mp4",
  ".webm",
  ".ogv",
  ".mov",
  ".avi",
  ".mkv",
  ".pdf",
  ".docx",
  ".pptx",
  ".xls",
  ".xlsx",
  ".xlsm",
  ".ods",
  ".zip",
  ".tar",
  ".gz",
  ".tgz",
  ".bz2",
  ".tbz2",
  ".rar",
  ".7z",
]);

export function isEditableText(name: string): boolean {
  const lower = name.toLowerCase();
  const base = lower.split("/").pop() ?? "";
  if (editableTextBasenames.has(base)) return true;
  const dot = base.lastIndexOf(".");
  if (dot < 0) return false;
  return editableTextExts.has(base.slice(dot));
}

export function shouldInspectTextFile(name: string): boolean {
  const lower = name.toLowerCase();
  const base = lower.split("/").pop() ?? "";
  if (base === "" || editableTextBasenames.has(base)) return false;
  const dot = base.lastIndexOf(".");
  if (dot < 0) return true;
  const ext = base.slice(dot);
  if (editableTextExts.has(ext)) return false;
  return !knownNonTextExts.has(ext);
}

// isSupportedArchive returns true when the filename ends with one of the
// archive extensions the backend's /files/extract endpoint accepts. Kept
// here next to the API surface so the context menu and the handler agree on
// the same whitelist; updates in one place, not two.
export function isSupportedArchive(name: string): boolean {
  const lower = name.toLowerCase();
  return (
    lower.endsWith(".zip") ||
    lower.endsWith(".tar") ||
    lower.endsWith(".tar.gz") ||
    lower.endsWith(".tgz") ||
    lower.endsWith(".tar.bz2") ||
    lower.endsWith(".tbz2")
  );
}
