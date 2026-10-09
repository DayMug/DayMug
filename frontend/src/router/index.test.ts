import { beforeEach, describe, expect, it, vi } from "vitest";

import router from "./index";

function setMobile(mobile: boolean) {
  vi.spyOn(window, "matchMedia").mockImplementation(
    (query: string) =>
      ({
        matches: mobile && query === "(max-width: 767px)",
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }) as unknown as MediaQueryList,
  );
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("responsive chat routes", () => {
  it("routes the desktop root and conversation list to /chat", async () => {
    setMobile(false);

    await router.push("/");
    expect(router.currentRoute.value.path).toBe("/chat");

    await router.push("/conversations");
    expect(router.currentRoute.value.path).toBe("/chat");
  });

  it("routes the mobile root to /conversations and preserves /chat", async () => {
    setMobile(true);

    await router.push("/");
    expect(router.currentRoute.value.path).toBe("/conversations");

    await router.push("/chat/user-1/conversation-1");
    expect(router.currentRoute.value.path).toBe("/chat/user-1/conversation-1");
  });

  it("exposes a dedicated scheduled-task settings route", async () => {
    setMobile(false);

    await router.push("/settings/crontab");
    expect(router.currentRoute.value.name).toBe("settings-crontab");
  });

  it("exposes the administrator panel and model registry routes", async () => {
    setMobile(false);

    await router.push("/settings/admin");
    expect(router.currentRoute.value.name).toBe("settings-admin");

    await router.push("/settings/admin/models");
    expect(router.currentRoute.value.name).toBe("settings-admin-models");
  });
});
