import { i18n } from "@/i18n";

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
