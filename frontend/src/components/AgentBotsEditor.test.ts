// @vitest-environment happy-dom
import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AgentBotsEditor from "./AgentBotsEditor.vue";

const api = vi.hoisted(() => ({
  fetchAgentBots: vi.fn(),
  fetchAgentBotStatuses: vi.fn(),
  fetchAgentBotRequirements: vi.fn(),
  createAgentBot: vi.fn(),
  updateAgentBot: vi.fn(),
  deleteAgentBot: vi.fn(),
  testAgentBotConnection: vi.fn(),
  startWeChatPairing: vi.fn(),
  pollWeChatPairing: vi.fn(),
}));
const modelApi = vi.hoisted(() => ({
  fetchModels: vi.fn(),
}));

vi.mock("@/composables/apiUsers", () => api);
vi.mock("@/composables/apiModels", () => modelApi);

describe("AgentBotsEditor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.fetchAgentBots.mockResolvedValue([
      {
        id: "b1",
        agent_id: "a1",
        name: "Slack Ops",
        platform: "slack",
        enabled: true,
        model: "claude-sonnet",
        max_conversation_duration: "3h",
        bot_token_configured: true,
        bot_app_token_configured: true,
        bot_app_id_configured: false,
        bot_app_secret_configured: false,
        credentials_configured: true,
        channels: '[{"channel":"*"}]',
        unconfigured_reply: "Slack not configured",
        unauthorized_reply: "Slack unauthorized",
      },
      {
        id: "b2",
        agent_id: "a1",
        name: "Feishu Ops",
        platform: "feishu",
        enabled: false,
        model: "",
        max_conversation_duration: "",
        bot_token_configured: false,
        bot_app_token_configured: false,
        bot_app_id_configured: true,
        bot_app_secret_configured: true,
        credentials_configured: true,
        channels: '[{"channel":"dm"}]',
        unconfigured_reply: "Feishu not configured",
        unauthorized_reply: "Feishu unauthorized",
      },
    ]);
    api.fetchAgentBotStatuses.mockResolvedValue({
      bots: [{ agent_id: "a1", bot_id: "b1", platform: "slack", running: true }],
    });
    api.fetchAgentBotRequirements.mockResolvedValue({
      slack: [
        { key: "connections:write", credential: "app_token", required: true },
        { key: "chat:write", credential: "bot_token", required: true },
      ],
      feishu: [
        { key: "im:message", credential: "app", required: true },
        { key: "contact:user.base:readonly", credential: "app", required: false },
      ],
    });
    modelApi.fetchModels.mockResolvedValue({
      providers: [
        {
          name: "claude",
          models: ["claude-sonnet", "claude-opus"],
          latest: "claude-sonnet",
          capabilities: {},
        },
      ],
      default_provider: "claude",
    });
  });

  it("renders multiple Bots attached to the same Agent", async () => {
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    expect(wrapper.text()).toContain("Slack Ops");
    expect(wrapper.text()).toContain("Feishu Ops");
    expect(api.fetchAgentBots).toHaveBeenCalledWith("a1");
  });

  // The agent form has three tabs and this editor is the third; a stale numeral
  // here contradicts the tab strip the user just clicked.
  it("numbers itself as the third section of the agent form", async () => {
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    expect(wrapper.get(".form-section-roman").text()).toBe("III.");
    expect(wrapper.get("section.form-section").attributes("id")).toBe("form-section-3");
  });

  // Credentials are write-only server-side, so the editor opens them blank and
  // relies on the *_configured flags to explain that something is stored.
  describe("write-only credentials", () => {
    async function openSavedSlackBot() {
      const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
      await flushPromises();
      const edit = wrapper.findAll("button").filter((button) => button.text() === "Edit")[0];
      await edit.trigger("click");
      return wrapper;
    }

    it("opens a saved bot with blank credential inputs marked as configured", async () => {
      const wrapper = await openSavedSlackBot();

      const token = wrapper.get('[data-testid="bot-token-input"]');
      expect((token.element as HTMLInputElement).value).toBe("");
      expect(token.attributes("placeholder")).toBe("Already configured — leave blank to keep it");
      expect(
        (wrapper.get('[data-testid="bot-app-token-input"]').element as HTMLInputElement).value,
      ).toBe("");
    });

    it("submits blank credentials when they are left untouched", async () => {
      api.updateAgentBot.mockResolvedValue({});
      const wrapper = await openSavedSlackBot();

      await wrapper.get("textarea").setValue('[{"channel":"dm"}]');
      const save = wrapper.findAll("button").filter((button) => button.text() === "Save")[0];
      await save.trigger("click");
      await flushPromises();

      expect(api.updateAgentBot).toHaveBeenCalledWith("a1", "b1", {
        name: "Slack Ops",
        platform: "slack",
        enabled: true,
        model: "claude-sonnet",
        max_conversation_duration: "3h",
        bot_token: "",
        bot_app_token: "",
        bot_app_id: "",
        bot_app_secret: "",
        channels: '[{"channel":"dm"}]',
        unconfigured_reply: "Slack not configured",
        unauthorized_reply: "Slack unauthorized",
      });
    });

    it("submits a credential the user retyped", async () => {
      api.updateAgentBot.mockResolvedValue({});
      const wrapper = await openSavedSlackBot();

      await wrapper.get('[data-testid="bot-token-input"]').setValue("xoxb-rotated");
      const save = wrapper.findAll("button").filter((button) => button.text() === "Save")[0];
      await save.trigger("click");
      await flushPromises();

      expect(api.updateAgentBot).toHaveBeenCalledWith(
        "a1",
        "b1",
        expect.objectContaining({ bot_token: "xoxb-rotated", bot_app_token: "" }),
      );
    });

    it("tests a saved bot by id instead of re-typing its secrets", async () => {
      api.testAgentBotConnection.mockResolvedValue({
        platform: "slack",
        connected: true,
        all_required_permissions_granted: true,
        permissions: [],
      });
      const wrapper = await openSavedSlackBot();

      const test = wrapper.get('[data-testid="test-bot-connection"]');
      expect(test.attributes("disabled")).toBeUndefined();
      await test.trigger("click");
      await flushPromises();

      expect(api.testAgentBotConnection).toHaveBeenCalledWith(
        "a1",
        expect.objectContaining({ bot_id: "b1", bot_token: "", bot_app_token: "" }),
      );
    });
  });

  it("enables a newly added Bot by default", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.createAgentBot.mockResolvedValue({});
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("input").setValue("Fresh bot");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(api.createAgentBot).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({ enabled: true }),
    );
  });

  it("keeps a saved Bot disabled when reopening it for edit", async () => {
    api.updateAgentBot.mockResolvedValue({});
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    // b2 is stored disabled; the new-bot default must not leak into edits.
    const edit = wrapper.findAll("button").filter((button) => button.text() === "Edit")[1];
    await edit.trigger("click");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(api.updateAgentBot).toHaveBeenCalledWith(
      "a1",
      "b2",
      expect.objectContaining({ enabled: false }),
    );
  });

  it("announces bot changes so the cached agent roster can be refreshed", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.createAgentBot.mockResolvedValue({});
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("input").setValue("Badge bot");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(wrapper.emitted("changed")).toHaveLength(1);
  });

  it("does not announce a bot change when the save failed", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.createAgentBot.mockRejectedValue(new Error("nope"));
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("input").setValue("Doomed bot");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(wrapper.emitted("changed")).toBeUndefined();
  });

  it("stores an independent model for new Bot threads", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.createAgentBot.mockResolvedValue({});
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("input").setValue("Model bot");
    await wrapper.get('[data-testid="bot-model"]').setValue("claude-opus");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(api.createAgentBot).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({ model: "claude-opus" }),
    );
  });

  it("stores a fixed maximum conversation duration", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.createAgentBot.mockResolvedValue({});
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="bot-name-input"]').setValue("Windowed bot");
    await wrapper.get('[data-testid="max-conversation-duration-input"]').setValue("3h");
    const save = wrapper.findAll("button").find((button) => button.text() === "Save");
    await save!.trigger("click");
    await flushPromises();

    expect(api.createAgentBot).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({ max_conversation_duration: "3h" }),
    );
  });

  it("starts a new Bot with the 12-hour conversation default", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");

    expect(
      (wrapper.get('[data-testid="max-conversation-duration-input"]').element as HTMLInputElement)
        .value,
    ).toBe("12h");
  });

  it("configures denial replies for new and saved bots", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    expect(
      (wrapper.get('[data-testid="unconfigured-reply-input"]').element as HTMLTextAreaElement)
        .value,
    ).toBe(
      "🙇 Sorry, I haven't been enabled in this conversation yet, so I can't reply to your message.",
    );
    expect(
      (wrapper.get('[data-testid="unauthorized-reply-input"]').element as HTMLTextAreaElement)
        .value,
    ).toBe("🙇 Sorry, I can't reply to your message right now.");

    await wrapper.get('[data-testid="unconfigured-reply-input"]').setValue("custom unconfigured");
    await wrapper.get('[data-testid="unauthorized-reply-input"]').setValue("custom unauthorized");
    await wrapper.get("input").setValue("New bot");
    api.createAgentBot.mockResolvedValue({});
    const save = wrapper.findAll("button").filter((button) => button.text() === "Save")[0];
    await save.trigger("click");
    await flushPromises();

    expect(api.createAgentBot).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({
        unconfigured_reply: "custom unconfigured",
        unauthorized_reply: "custom unauthorized",
      }),
    );
  });

  it("opens a complete channel rules example from the help icon", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, {
      props: { agentId: "a1" },
      attachTo: document.body,
    });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="channel-rules-help"]').trigger("click");
    await flushPromises();

    const example = document.querySelector('[data-testid="channel-rules-example"]');
    expect(example?.textContent).toContain('"channel": "C0123456789"');
    expect(example?.textContent).toContain('"channel": "*"');
    expect(example?.textContent).toContain('"channel": "dm"');
    expect(example?.textContent).toContain('"allow_bot_mentions": true');
    expect(example?.textContent).toContain('"allowed_user_ids": [');
    const help = document.querySelector('[role="dialog"]')?.textContent ?? "";
    expect(help).toContain("allowed_user_ids");
    expect(help).toContain("bot_mention_limit");
    expect(help).toContain("bot_mention_window_minutes");

    wrapper.unmount();
  });

  it("documents every Bot setting and the fixed conversation boundary", async () => {
    const wrapper = mount(AgentBotsEditor, {
      props: { agentId: "a1" },
      attachTo: document.body,
    });
    await flushPromises();

    await wrapper.get('[data-testid="bot-config-help"]').trigger("click");
    await flushPromises();

    const help = document.querySelector('[role="dialog"]')?.textContent ?? "";
    expect(help).toContain("name");
    expect(help).toContain("platform");
    expect(help).toContain("credentials");
    expect(help).toContain("max_conversation_duration");
    expect(help).toContain("fresh CLI session");
    expect(help).toContain("follow-ups never extend it");

    wrapper.unmount();
  });

  it("documents Bot-level denial replies in the channel help dialog", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, {
      props: { agentId: "a1" },
      attachTo: document.body,
    });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="channel-rules-help"]').trigger("click");
    await flushPromises();

    const help = document.querySelector('[data-testid="denial-replies-help"]');
    expect(help?.textContent).toContain("unconfigured_reply");
    expect(help?.textContent).toContain("unauthorized_reply");
    expect(help?.textContent).toContain("does not run the Agent");
    expect(help?.textContent).toContain("enabled: false");

    wrapper.unmount();
  });

  it("explains why messages may stay silent in the channel rules help", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, {
      props: { agentId: "a1" },
      attachTo: document.body,
    });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="channel-rules-help"]').trigger("click");
    await flushPromises();

    const troubleshooting = document.querySelector('[data-testid="channel-rules-troubleshooting"]');
    expect(troubleshooting?.textContent).toContain("Why did the bot not reply?");
    expect(troubleshooting?.textContent).toContain("require_mention");
    expect(troubleshooting?.textContent).toContain('"auto_reply": true');
    expect(troubleshooting?.textContent).toContain("allowed_user_ids");
    expect(troubleshooting?.textContent).toContain("allow_bot_mentions");

    wrapper.unmount();
  });

  it("aligns the channel rules help icon with its label", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");

    const helpButton = wrapper.get('[data-testid="channel-rules-help"]');
    expect(helpButton.element.parentElement?.classList).toContain("items-center");
    expect(helpButton.element.previousElementSibling?.classList).toContain("!mb-0");
  });

  it("uses a high-contrast text selection in the channel rules dialog", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, {
      props: { agentId: "a1" },
      attachTo: document.body,
    });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="channel-rules-help"]').trigger("click");
    await flushPromises();

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.classList).toContain("selection:bg-primary");
    expect(dialog?.classList).toContain("selection:text-primary-foreground");

    wrapper.unmount();
  });

  it("shows every required platform permission before testing", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");

    expect(wrapper.get('[data-testid="bot-permissions"]').text()).toContain("connections:write");
    expect(wrapper.get('[data-testid="bot-permissions"]').text()).toContain("chat:write");

    await wrapper.get("select").setValue("feishu");

    expect(wrapper.get('[data-testid="bot-permissions"]').text()).toContain("im:message");
    expect(wrapper.get('[data-testid="bot-permissions"]').text()).toContain(
      "contact:user.base:readonly",
    );
    expect(wrapper.get('[data-testid="bot-permissions"]').text()).toContain("Optional");
  });

  it("tests draft credentials and marks missing permissions", async () => {
    api.fetchAgentBots.mockResolvedValue([]);
    api.testAgentBotConnection.mockResolvedValue({
      platform: "slack",
      connected: true,
      identity: "Acme / DayMug",
      all_required_permissions_granted: false,
      permissions: [
        { key: "connections:write", credential: "app_token", required: true, granted: true },
        { key: "chat:write", credential: "bot_token", required: true, granted: false },
      ],
    });
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get('[data-testid="bot-token-input"]').setValue("xoxb-test");
    await wrapper.get('[data-testid="bot-app-token-input"]').setValue("xapp-test");
    await wrapper.get('[data-testid="test-bot-connection"]').trigger("click");
    await flushPromises();

    expect(api.testAgentBotConnection).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({
        platform: "slack",
        bot_token: "xoxb-test",
        bot_app_token: "xapp-test",
      }),
    );
    expect(wrapper.get('[data-testid="connection-test-result"]').text()).toContain(
      "required permissions are missing",
    );
    expect(wrapper.findAll('[aria-label="Granted"]')).toHaveLength(1);
    expect(wrapper.findAll('[aria-label="Missing"]')).toHaveLength(1);
  });
  it("asks Telegram for its one credential and nothing else", async () => {
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("select").setValue("telegram");

    // Slack's app-level token and 飞书's app id/secret must disappear, leaving
    // only the bot token @BotFather issues.
    expect(wrapper.find('[data-testid="bot-app-token-input"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="bot-app-id-input"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="bot-app-secret-input"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="bot-token-input"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("@BotFather");
  });

  it("tests a Telegram connection with only the bot token", async () => {
    api.testAgentBotConnection.mockResolvedValue({
      platform: "telegram",
      connected: true,
      identity: "@daymugbot",
      all_required_permissions_granted: true,
      permissions: [{ key: "bot_token", credential: "bot_token", required: true, granted: true }],
    });

    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("select").setValue("telegram");
    await wrapper.get('[data-testid="bot-token-input"]').setValue("123456:ABC");
    await wrapper.get('[data-testid="test-bot-connection"]').trigger("click");
    await flushPromises();

    expect(api.testAgentBotConnection).toHaveBeenCalledWith(
      "a1",
      expect.objectContaining({ platform: "telegram", bot_token: "123456:ABC" }),
    );
  });

  it("sample channel rules use Telegram's numeric chat ids", async () => {
    const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
    await flushPromises();

    await wrapper.get("button").trigger("click");
    await wrapper.get("select").setValue("telegram");
    await wrapper.get('[data-testid="channel-rules-help"]').trigger("click");
    await flushPromises();

    expect(document.body.textContent).toContain("-1001234567890");
  });

  // WeChat credentials are produced by a QR handshake rather than typed, so the
  // editor has to drive the pairing itself and drop the result into the generic
  // credential slots.
  describe("WeChat pairing", () => {
    async function openNewWeChatBot() {
      const wrapper = mount(AgentBotsEditor, { props: { agentId: "a1" } });
      await flushPromises();
      const add = wrapper.findAll("button").filter((b) => b.text().includes("Add bot"))[0];
      await add.trigger("click");
      await wrapper.get("select").setValue("wechat");
      await flushPromises();
      return wrapper;
    }

    it("allows every direct-message sender by default", async () => {
      const wrapper = await openNewWeChatBot();

      const channels = JSON.parse((wrapper.get("textarea").element as HTMLTextAreaElement).value);
      expect(channels).toEqual([{ channel: "dm", allowed_user_ids: ["*"] }]);
    });

    it("offers pairing instead of credential inputs", async () => {
      const wrapper = await openNewWeChatBot();

      expect(wrapper.find('[data-testid="wechat-pairing"]').exists()).toBe(true);
      // A token field would invite pasting a value that is useless without the
      // gateway issued alongside it.
      expect(wrapper.find('[data-testid="bot-token-input"]').exists()).toBe(false);
    });

    it("fills both credential slots once the scan is confirmed", async () => {
      api.startWeChatPairing.mockResolvedValue({
        challenge: "chal-1",
        qr_image: "UE5H",
        qr_content: "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=chal-1",
      });
      // The real status endpoint long-polls, so the first answer is "pending".
      api.pollWeChatPairing.mockResolvedValueOnce({ status: "pending" }).mockResolvedValue({
        status: "confirmed",
        bot_token: "tok",
        base_url: "https://acct.example",
      });
      api.createAgentBot.mockResolvedValue({});

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();

      expect(wrapper.find('[data-testid="wechat-pairing-confirmed"]').exists()).toBe(true);

      await wrapper.get('[data-testid="bot-name-input"]').setValue("WeChat Ops");
      const save = wrapper.findAll("button").filter((b) => b.text() === "Save")[0];
      await save.trigger("click");
      await flushPromises();

      // The gateway rides in bot_app_id: the token alone cannot be routed.
      expect(api.createAgentBot).toHaveBeenCalledWith(
        "a1",
        expect.objectContaining({
          platform: "wechat",
          bot_token: "tok",
          bot_app_id: "https://acct.example",
        }),
      );
    });

    it("does not write a late confirmation into another bot opened meanwhile", async () => {
      api.startWeChatPairing.mockResolvedValue({ challenge: "chal-1", qr_image: "UE5H" });
      let releasePoll: (value: Record<string, string>) => void = () => {};
      api.pollWeChatPairing.mockReturnValue(
        new Promise<Record<string, string>>((resolve) => {
          releasePoll = resolve;
        }),
      );
      api.updateAgentBot.mockResolvedValue({});

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();

      // Switch to the saved Slack bot while the scan is still pending.
      const edit = wrapper.findAll("button").filter((b) => b.text() === "Edit")[0];
      await edit.trigger("click");
      releasePoll({ status: "confirmed", bot_token: "wx-tok", base_url: "https://acct.example" });
      await flushPromises();

      expect(
        (wrapper.get('[data-testid="bot-token-input"]').element as HTMLInputElement).value,
      ).toBe("");
      const save = wrapper.findAll("button").filter((b) => b.text() === "Save")[0];
      await save.trigger("click");
      await flushPromises();
      expect(api.updateAgentBot).toHaveBeenCalledWith(
        "a1",
        "b1",
        expect.objectContaining({ platform: "slack", bot_token: "", bot_app_id: "" }),
      );
    });

    it("shows the QR code while a poll is in flight", async () => {
      api.startWeChatPairing.mockResolvedValue({
        challenge: "chal-1",
        qr_image: "UE5H",
        qr_content: "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=chal-1",
      });
      // Held open the way the real endpoint holds a request for ~30s.
      let releasePoll: (value: { status: string }) => void = () => {};
      api.pollWeChatPairing.mockReturnValue(
        new Promise<{ status: string }>((resolve) => {
          releasePoll = resolve;
        }),
      );

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();

      expect(wrapper.get('[data-testid="wechat-qr"]').attributes("src")).toBe(
        "data:image/png;base64,UE5H",
      );
      // The scan URL is offered too, for when the image will not render.
      expect(wrapper.get('[data-testid="wechat-scan-url"]').attributes("href")).toBe(
        "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=chal-1",
      );
      expect(wrapper.find('[data-testid="wechat-pairing-waiting"]').exists()).toBe(true);

      releasePoll({ status: "expired" });
      await flushPromises();
      expect(wrapper.find('[data-testid="wechat-qr"]').exists()).toBe(false);
    });

    // The endpoint parks for ~30s per call, so the client must re-issue it at
    // once rather than adding idle time on top of the wait.
    it("re-polls immediately instead of waiting out a timer", async () => {
      api.startWeChatPairing.mockResolvedValue({ challenge: "chal-1", qr_image: "UE5H" });
      api.pollWeChatPairing
        .mockResolvedValueOnce({ status: "pending" })
        .mockResolvedValueOnce({ status: "scanned" })
        .mockResolvedValue({ status: "confirmed", bot_token: "t", base_url: "u" });

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();

      expect(api.pollWeChatPairing.mock.calls.length).toBeGreaterThanOrEqual(3);
      expect(wrapper.find('[data-testid="wechat-pairing-confirmed"]').exists()).toBe(true);
    });

    // WeChat asking for a verification code cannot be satisfied here, so it
    // must stop and say so rather than look like an ordinary expiry.
    it("stops when WeChat demands a verification code", async () => {
      api.startWeChatPairing.mockResolvedValue({ challenge: "chal-1", qr_image: "UE5H" });
      api.pollWeChatPairing.mockResolvedValue({ status: "blocked" });

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();

      expect(wrapper.find('[data-testid="wechat-pairing-blocked"]').exists()).toBe(true);
      const calls = api.pollWeChatPairing.mock.calls.length;
      await flushPromises();
      expect(api.pollWeChatPairing.mock.calls.length).toBe(calls);
    });

    it("reports an expired challenge instead of polling forever", async () => {
      api.startWeChatPairing.mockResolvedValue({ challenge: "chal-1", qr_image: "UE5H" });
      api.pollWeChatPairing.mockResolvedValue({ status: "expired" });
      vi.useFakeTimers();
      try {
        const wrapper = await openNewWeChatBot();
        await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
        await flushPromises();

        expect(wrapper.find('[data-testid="wechat-pairing-expired"]').exists()).toBe(true);
        const calls = api.pollWeChatPairing.mock.calls.length;
        await vi.advanceTimersByTimeAsync(10_000);
        expect(api.pollWeChatPairing.mock.calls.length).toBe(calls);
      } finally {
        vi.useRealTimers();
      }
    });

    // The status endpoint re-issues itself after every long-poll, so closing
    // the editor has to break that chain, not just a pending retry timer.
    it("stops polling once the editor is unmounted", async () => {
      api.startWeChatPairing.mockResolvedValue({ challenge: "chal-1", qr_image: "UE5H" });
      let releasePoll: (value: { status: string }) => void = () => {};
      api.pollWeChatPairing
        .mockReturnValueOnce(
          new Promise<{ status: string }>((resolve) => {
            releasePoll = resolve;
          }),
        )
        .mockResolvedValue({ status: "pending" });

      const wrapper = await openNewWeChatBot();
      await wrapper.get('[data-testid="wechat-pair-button"]').trigger("click");
      await flushPromises();
      expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);

      wrapper.unmount();
      releasePoll({ status: "pending" });
      await flushPromises();
      expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);
    });
  });
});
