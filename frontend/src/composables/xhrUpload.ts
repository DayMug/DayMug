import { UnauthorizedError, notifyUnauthorized } from "./apiClient";

export interface UploadProgress {
  loaded: number;
  total: number;
}

export interface XhrUploadOptions {
  onProgress?: (progress: UploadProgress) => void;
  signal?: AbortSignal;
}

// xhrUpload POSTs a multipart body with the session cookie. XHR rather than
// fetch because fetch has no upload-progress signal until the streaming
// request body API is broadly available.
//
// It owns only the transport: progress, AbortSignal cancellation (rejects with
// an AbortError DOMException), network failure, and the app-wide 401 handling.
// Any other response resolves with the finished request so each caller maps
// its endpoint's statuses (409 conflicts, 413 proxy limits, JSON error bodies)
// itself.
export function xhrUpload(
  url: string,
  body: FormData,
  options?: XhrUploadOptions,
): Promise<XMLHttpRequest> {
  return new Promise<XMLHttpRequest>((resolve, reject) => {
    if (options?.signal?.aborted) {
      reject(new DOMException("aborted", "AbortError"));
      return;
    }
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.withCredentials = true;
    const onProgress = options?.onProgress;
    if (onProgress) {
      xhr.upload.addEventListener("progress", (e) => {
        if (e.lengthComputable) onProgress({ loaded: e.loaded, total: e.total });
      });
    }
    const onAbort = () => xhr.abort();
    options?.signal?.addEventListener("abort", onAbort);
    const cleanup = () => options?.signal?.removeEventListener("abort", onAbort);
    xhr.addEventListener("load", () => {
      cleanup();
      if (xhr.status === 401) {
        notifyUnauthorized();
        reject(new UnauthorizedError());
        return;
      }
      resolve(xhr);
    });
    xhr.addEventListener("error", () => {
      cleanup();
      reject(new Error("upload: network error"));
    });
    xhr.addEventListener("abort", () => {
      cleanup();
      reject(new DOMException("aborted", "AbortError"));
    });
    xhr.send(body);
  });
}
