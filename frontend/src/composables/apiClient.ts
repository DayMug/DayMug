import { apiBase } from "./apiBase";

export class UnauthorizedError extends Error {
  status = 401;
  constructor(message = "unauthorized") {
    super(message);
    this.name = "UnauthorizedError";
  }
}

type UnauthorizedHandler = (() => void) | null;

let onUnauthorized: UnauthorizedHandler = null;

export function setUnauthorizedHandler(handler: UnauthorizedHandler) {
  onUnauthorized = handler;
}

function isAuthEndpoint(input: string): boolean {
  // Login attempts return their own 401 (bad credentials) — don't redirect.
  return input.includes("/api/auth/login");
}

export async function apiFetch(input: string, init?: RequestInit): Promise<Response> {
  const url = input.startsWith("http") ? input : `${apiBase()}${input}`;
  const res = await fetch(url, init);
  if (res.status === 401 && !isAuthEndpoint(url)) {
    onUnauthorized?.();
    throw new UnauthorizedError();
  }
  return res;
}

export function jsonRequestInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

// ApiError is what request() throws for a non-2xx response. `status` lets
// callers branch on the HTTP code (409 move conflicts, 404 in file polling)
// without parsing the message; `body` is the decoded JSON error envelope when
// there was one, for endpoints that return extra fields next to `error`.
export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

// ErrorBody picks how the backend's JSON `error` field shapes the message
// (the decoded body always rides along on ApiError.body). Each mode is a
// format the UI already renders, so they must stay distinct:
//   - "ignore":  `${label}: ${status}`, whatever the body says.
//   - "message": the body's `error`, else `${label}: ${status}`
//                (or the bare status when there is no label).
//   - "suffix":  `${label}: ${status}: ${error}`, else `${label}: ${status}`.
export type ErrorBody = "ignore" | "message" | "suffix";

export interface RequestOptions {
  label?: string;
  errorBody?: ErrorBody;
  // "json" (default) decodes the success body; "none" resolves undefined
  // without reading it; "response" hands back the raw Response for streaming
  // or binary reads.
  expect?: "json" | "none" | "response";
}

async function readErrorBody(res: Response): Promise<unknown> {
  try {
    return await res.json();
  } catch {
    return undefined;
  }
}

function errorReason(body: unknown): string {
  if (body && typeof body === "object" && "error" in body) {
    const reason = (body as { error?: unknown }).error;
    if (reason) return String(reason);
  }
  return "";
}

// apiErrorFrom builds the ApiError for a failed response. Exposed for the XHR
// upload paths, which have a status and body but no fetch Response of their
// own.
export async function apiErrorFrom(res: Response, opts: RequestOptions = {}): Promise<ApiError> {
  const body = await readErrorBody(res);
  const statusText = opts.label ? `${opts.label}: ${res.status}` : `${res.status}`;
  const reason = errorReason(body);
  let message = statusText;
  if (reason && opts.errorBody === "message") message = reason;
  if (reason && opts.errorBody === "suffix") message = `${statusText}: ${reason}`;
  return new ApiError(message, res.status, body);
}

// request is the one entry point for JSON API calls: it runs apiFetch (so the
// app-wide 401 redirect applies), turns a non-2xx response into an ApiError
// formatted per `opts`, and decodes the success body.
export async function request<T>(
  input: string,
  init: RequestInit | undefined,
  opts: RequestOptions = {},
): Promise<T> {
  const res = await apiFetch(input, init);
  if (!res.ok) throw await apiErrorFrom(res, opts);
  switch (opts.expect) {
    case "none":
      return undefined as T;
    case "response":
      return res as T;
    default:
      return res.json();
  }
}

// Lets non-fetch callers (XMLHttpRequest paths used for upload progress)
// run the same 401 redirect logic apiFetch does.
export function notifyUnauthorized() {
  onUnauthorized?.();
}
