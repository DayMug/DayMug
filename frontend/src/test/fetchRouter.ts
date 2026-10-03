type JsonValue = Record<string, unknown> | unknown[];

const passiveRoutes: Array<{ pattern: RegExp; body: JsonValue }> = [
  {
    pattern: /^\/api\/models$/,
    body: { providers: [], accounts: [], default_provider: "" },
  },
  {
    pattern: /^\/api\/server-info$/,
    body: {
      version: "test",
      sandbox_enabled: false,
      sandbox_type: "",
      manifest_url: "",
    },
  },
  {
    pattern: /^\/api\/help-doc$/,
    body: { markdown: "" },
  },
  {
    pattern: /^\/api\/conversations\/[^/]+\/commands$/,
    body: { commands: [] },
  },
];

export function createTestFetchRouter(): {
  fetch: typeof fetch;
  unexpectedRequests: string[];
} {
  const unexpectedRequests: string[] = [];
  const testFetch: typeof fetch = async (input) => {
    const url = new URL(input instanceof Request ? input.url : String(input), "http://localhost");
    const route = passiveRoutes.find(({ pattern }) => pattern.test(url.pathname));
    if (!route) {
      unexpectedRequests.push(`${url.pathname}${url.search}`);
      throw new Error(`Unexpected fetch in test: ${url.pathname}${url.search}`);
    }
    return new Response(JSON.stringify(route.body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { fetch: testFetch, unexpectedRequests };
}
