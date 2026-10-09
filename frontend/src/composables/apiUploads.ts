import { apiBase } from "./apiBase";
import { apiErrorFrom } from "./apiClient";
import type { UploadResult } from "./apiTypes";
import { xhrUpload, type XhrUploadOptions } from "./xhrUpload";
import { friendlyUploadErrorMessage } from "@/lib/uploadHelpers";

export type { UploadProgress } from "./xhrUpload";
export type UploadOptions = XhrUploadOptions;

// Progress + mid-flight cancel for the chat composer, the same per-file
// feedback the workspace uploader shows (see xhrUpload).
export async function uploadFile(
  file: Blob,
  conversationId: string,
  filename?: string,
  options?: UploadOptions,
): Promise<UploadResult> {
  const fd = new FormData();
  // Anonymous Blobs (e.g. clipboard pastes) carry no name; default to a
  // placeholder so the multipart parser doesn't reject the part.
  fd.append("file", file, filename ?? (file as File).name ?? "pasted-file");
  fd.append("conversation_id", conversationId);
  const url = `${apiBase()}/api/uploads`;

  const xhr = await xhrUpload(url, fd, options);
  if (xhr.status >= 200 && xhr.status < 300) {
    return JSON.parse(xhr.responseText) as UploadResult;
  }
  // 413 can come from DayMug's own cap OR from a reverse proxy in front
  // (Nginx's `client_max_body_size` is the usual culprit). The proxy's
  // body is typically an HTML error page, not our JSON envelope — so
  // skip the JSON error envelope and surface a localized hint that explicitly
  // names the proxy as a possible source.
  const friendly = friendlyUploadErrorMessage(xhr.status);
  if (friendly) throw new Error(friendly);
  // Same contract as request(): the JSON `error` field becomes the message,
  // falling back to a code.
  throw await apiErrorFrom(new Response(xhr.responseText, { status: xhr.status }), {
    label: "upload",
    errorBody: "message",
  });
}
