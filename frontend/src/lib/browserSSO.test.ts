import { afterEach, expect, it, vi } from "vitest";
import { browserSSO } from "./browserSSO";

afterEach(() => {
  vi.unstubAllGlobals();
  delete window.__DAYMUG_CLIENT__;
});
it("opens the IdP externally and claims the session without exposing the claim in its URL", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        auth_url: "https://sso.example.com/login?state=browser",
        claim: "private",
      }),
    })
    .mockResolvedValueOnce({ status: 204 });
  vi.stubGlobal("fetch", fetch);
  const open = vi.spyOn(window, "open").mockReturnValue(null);
  await browserSSO(new AbortController().signal);
  expect(open).toHaveBeenCalledWith(
    "https://sso.example.com/login?state=browser",
    "_blank",
    "noopener,noreferrer",
  );
  expect(fetch.mock.calls[1]?.[1].body).toBe(JSON.stringify({ claim: "private" }));
  open.mockRestore();
});
it("surfaces provider denial", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ auth_url: "https://sso.example.com", claim: "private" }),
      })
      .mockResolvedValueOnce({ status: 401, json: async () => ({ error: "access denied" }) }),
  );
  const open = vi.spyOn(window, "open").mockReturnValue(null);
  await expect(browserSSO(new AbortController().signal)).rejects.toThrow("access denied");
  open.mockRestore();
});

it("uses native browser navigation when advertised by the client", async () => {
  window.__DAYMUG_CLIENT__ = { nonce: "n".repeat(40), browserLoginNavigation: true };
  const assign = vi.fn();
  vi.stubGlobal("location", { assign });
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ auth_url: "https://sso.example.com/login", claim: "private" }),
      })
      .mockResolvedValueOnce({ status: 204 }),
  );
  await browserSSO(new AbortController().signal);
  expect(assign).toHaveBeenCalledWith(
    "/__daymug_client__/browser-login?url=https%3A%2F%2Fsso.example.com%2Flogin",
  );
});
