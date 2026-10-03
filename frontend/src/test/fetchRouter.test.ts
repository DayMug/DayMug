import { describe, expect, it } from "vitest";

import { createTestFetchRouter } from "./fetchRouter";

describe("test fetch router", () => {
  it("serves passive component dependencies without touching the network", async () => {
    const router = createTestFetchRouter();
    const response = await router.fetch("/api/models");

    await expect(response.json()).resolves.toEqual({
      providers: [],
      accounts: [],
      default_provider: "",
    });
    expect(router.unexpectedRequests).toEqual([]);
  });

  it("serves the optional help document as an empty passive dependency", async () => {
    const router = createTestFetchRouter();
    const response = await router.fetch("/api/help-doc");

    await expect(response.json()).resolves.toEqual({ markdown: "" });
    expect(router.unexpectedRequests).toEqual([]);
  });

  it("records and rejects requests that a test did not explicitly mock", async () => {
    const router = createTestFetchRouter();

    await expect(router.fetch("/api/users")).rejects.toThrow("Unexpected fetch in test");
    expect(router.unexpectedRequests).toEqual(["/api/users"]);
  });
});
