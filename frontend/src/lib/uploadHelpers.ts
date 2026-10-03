import { i18n } from "@/i18n";

// Per-top-level-folder file count.
// `webkitRelativePath` (folder picker) and the synthetic `name` we set during
// drag-drop traversal both encode the path as `<top>/<sub>/.../file`. Files
// outside any folder (single-file picks) are skipped — the 10-file limit only
// applies to folder uploads.
export function countFilesPerTopFolder(
  files: { webkitRelativePath?: string; name: string }[],
): Map<string, number> {
  const counts = new Map<string, number>();
  for (const f of files) {
    const path = f.webkitRelativePath || f.name;
    const slash = path.indexOf("/");
    if (slash <= 0) continue;
    const top = path.slice(0, slash);
    counts.set(top, (counts.get(top) ?? 0) + 1);
  }
  return counts;
}

export const FOLDER_FILE_LIMIT = 10;

// Returns a localized message when any uploaded folder exceeds the limit, or null.
export function checkFolderFileLimit(
  files: { webkitRelativePath?: string; name: string }[],
  limit: number = FOLDER_FILE_LIMIT,
): string | null {
  const counts = countFilesPerTopFolder(files);
  for (const [name, count] of counts) {
    if (count > limit) {
      return `Folder "${name}" contains ${count} files (including subdirectories), exceeding the limit of ${limit}. Please compress it to a zip before uploading.`;
    }
  }
  return null;
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export function formatSpeed(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`;
}

// friendlyUploadErrorMessage maps HTTP status codes the upload endpoints
// can fail with into a localized, user-actionable string. Today only 413
// (Payload Too Large) is special-cased: it is the canonical signal that
// either DayMug's own per-file cap or — more commonly — a reverse-proxy
// `client_max_body_size` rejected the request. Returns null for statuses
// we don't have a friendlier hint for, so the caller can keep its current
// fallback message.
export function friendlyUploadErrorMessage(status: number): string | null {
  if (status === 413) return i18n.global.t("upload.errors.tooLarge");
  return null;
}
