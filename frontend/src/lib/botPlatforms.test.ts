import { describe, expect, it } from "vitest";

import {
  BOT_PLATFORM_IDS,
  BOT_PLATFORMS,
  botPlatformLabelKey,
  defaultChannelsFor,
} from "./botPlatforms";

describe("botPlatforms", () => {
  it("lists every platform in the table, in table order", () => {
    expect(BOT_PLATFORM_IDS).toEqual(["slack", "feishu", "telegram", "wechat"]);
  });

  it("names known platforms and leaves unknown ones to the caller", () => {
    expect(botPlatformLabelKey("feishu")).toBe("settings.agentForm.platformFeishu");
    expect(botPlatformLabelKey("matrix")).toBeNull();
    expect(botPlatformLabelKey("toString")).toBeNull();
  });

  it("gives WeChat a DM-only default rule and every other platform a mention rule", () => {
    expect(JSON.parse(defaultChannelsFor("wechat"))).toEqual([
      { channel: "dm", allowed_user_ids: ["*"] },
    ]);
    for (const platform of ["slack", "feishu", "telegram"] as const) {
      expect(JSON.parse(defaultChannelsFor(platform))).toEqual([
        { channel: "*", require_mention: true },
      ]);
    }
  });

  it("pairs WeChat instead of asking for typed credentials", () => {
    expect(BOT_PLATFORMS.wechat.credentials).toEqual([]);
    for (const platform of ["slack", "feishu", "telegram"] as const) {
      expect(BOT_PLATFORMS[platform].credentials.length).toBeGreaterThan(0);
    }
  });
});
