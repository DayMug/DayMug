import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  ApiError,
  apiErrorFrom,
  apiFetch,
  request,
  setUnauthorizedHandler,
  UnauthorizedError,
} from "./apiClient";

beforeEach(() => {
  vi.restoreAllMocks();
});

afterEach(() => {
  setUnauthorizedHandler(null);
});

describe("apiFetch", () => {
  it("returns response on success", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({}) }),
    );
    const res = await apiFetch("/api/users");
    expect(res.ok).toBe(true);
    expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/api/users"), undefined);
  });

  it("invokes unauthorized handler and throws on 401", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 401 }));
    const handler = vi.fn();
    setUnauthorizedHandler(handler);

    await expect(apiFetch("/api/users")).rejects.toBeInstanceOf(UnauthorizedError);
    expect(handler).toHaveBeenCalledOnce();
  });

  it("does not invoke handler for non-401 errors", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    const handler = vi.fn();
    setUnauthorizedHandler(handler);

    const res = await apiFetch("/api/users");
    expect(res.status).toBe(500);
    expect(handler).not.toHaveBeenCalled();
  });

  it("does not redirect when login endpoint returns 401", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 401 }));
    const handler = vi.fn();
    setUnauthorizedHandler(handler);

    const res = await apiFetch("/api/auth/login", { method: "POST" });
    expect(res.status).toBe(401);
    expect(handler).not.toHaveBeenCalled();
  });

  it("forwards init options to fetch", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200 }));
    await apiFetch("/api/users", { method: "POST", body: "x" });
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/users"),
      expect.objectContaining({ method: "POST", body: "x" }),
    );
  });
});

function stubResponse(status: number, body: unknown) {
  const json = vi.fn(() =>
    body === undefined ? Promise.reject(new Error("not json")) : Promise.resolve(body),
  );
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: status < 300, status, json }));
  return json;
}

describe("request", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("decodes the JSON body by default", async () => {
    stubResponse(200, { a: 1 });
    await expect(request("/api/x", undefined, { label: "x" })).resolves.toEqual({ a: 1 });
  });

  it("skips the body with expect: none", async () => {
    const json = stubResponse(204, {});
    await expect(request<void>("/api/x", undefined, { expect: "none" })).resolves.toBeUndefined();
    expect(json).not.toHaveBeenCalled();
  });

  it("returns the raw response with expect: response", async () => {
    stubResponse(200, {});
    const res = await request<Response>("/api/x", undefined, { expect: "response" });
    expect(res.status).toBe(200);
  });

  it("throws ApiError carrying status and decoded body", async () => {
    stubResponse(412, { error: "stale", etag: "v2" });
    const err = await request("/api/x", undefined, { label: "x" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toBeInstanceOf(Error);
    expect((err as ApiError).status).toBe(412);
    expect((err as ApiError).body).toEqual({ error: "stale", etag: "v2" });
    // Keeps the plain "Error" name so String(err) reads as it did before.
    expect(String(err)).toBe("Error: x: 412");
  });
});

describe("apiErrorFrom message formats", () => {
  const withReason = () => new Response(JSON.stringify({ error: "why" }), { status: 400 });
  const noReason = () => new Response("<html>", { status: 400 });

  it.each([
    ["ignore + label", { label: "op" }, withReason, "op: 400"],
    ["message + label, reason", { label: "op", errorBody: "message" }, withReason, "why"],
    ["message + label, no reason", { label: "op", errorBody: "message" }, noReason, "op: 400"],
    ["message, no label, reason", { errorBody: "message" }, withReason, "why"],
    ["message, no label, no reason", { errorBody: "message" }, noReason, "400"],
    ["suffix, reason", { label: "op", errorBody: "suffix" }, withReason, "op: 400: why"],
    ["suffix, no reason", { label: "op", errorBody: "suffix" }, noReason, "op: 400"],
  ] as const)("%s", async (_name, opts, res, want) => {
    const err = await apiErrorFrom(res(), opts);
    expect(err.message).toBe(want);
    expect(err.status).toBe(400);
  });
});
