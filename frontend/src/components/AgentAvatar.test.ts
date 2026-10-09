import { describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import AgentAvatar from "./AgentAvatar.vue";
import agentAvatarSource from "./AgentAvatar.vue?raw";

describe("AgentAvatar", () => {
  it.each(["done", "waiting", "error"] as const)(
    "places a %s attention dot on the logo's top-right corner",
    (attention) => {
      const wrapper = mount(AgentAvatar, {
        props: { name: "Release Agent", attention },
      });

      const dot = wrapper.get('[data-testid="unread-completion-agent-dot"]');
      expect(dot.classes()).toEqual(expect.arrayContaining(["-right-1", "-top-1", "size-[8px]"]));
      expect(dot.attributes("data-attention")).toBe(attention);
    },
  );

  it("shows no attention dot without an attention state", () => {
    const wrapper = mount(AgentAvatar, { props: { name: "Release Agent" } });

    expect(wrapper.find('[data-testid="unread-completion-agent-dot"]').exists()).toBe(false);
  });

  it("keeps initials as the default agent avatar", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Release Agent" },
    });

    expect(wrapper.text()).toContain("RA");
    expect(wrapper.get('[data-slot="avatar-fallback"]').classes()).toContain("rounded-[4px]");
  });

  it("renders an http avatar URL as an image without avatar text", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Release Agent", avatar: "https://example.com/avatar.png" },
    });

    const image = wrapper.get('[data-slot="avatar-image"]');
    expect(image.attributes()).toMatchObject({
      src: "https://example.com/avatar.png",
      alt: "Release Agent",
    });
    expect(wrapper.find('[data-slot="avatar-fallback"]').exists()).toBe(false);
    expect(wrapper.text()).toBe("");
  });

  it("keeps non-URL avatar values as text", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Release Agent", avatar: "RA" },
    });

    expect(wrapper.find('[data-slot="avatar-image"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("RA");
  });

  it("recreates avatar state when switching from an image agent to initials", async () => {
    const wrapper = mount(AgentAvatar, {
      props: {
        name: "Image Agent",
        avatar: "https://example.com/avatar.png",
      },
    });

    expect(wrapper.find('[data-slot="avatar-image"]').exists()).toBe(true);

    await wrapper.setProps({ name: "Daymug", avatar: "" });
    await flushPromises();

    expect(wrapper.find('[data-slot="avatar-image"]').exists()).toBe(false);
    expect(wrapper.get('[data-slot="avatar-fallback"]').text()).toContain("D");
  });

  it("marks agents that have connected bots with a distinct badge", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", botPlatforms: ["slack", "feishu"] },
    });
    const badge = wrapper.find('[data-testid="bot-connected-avatar"]');
    expect(badge.exists()).toBe(true);
    expect(badge.attributes("title")).toBe("slack · feishu");
    expect(badge.classes()).toContain("bg-emerald-600");
  });

  it("keeps plain agents badge-free", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", botPlatforms: [] },
    });
    expect(wrapper.find('[data-testid="bot-connected-avatar"]').exists()).toBe(false);
    expect(wrapper.get('[data-slot="avatar"]').classes()).toContain("overflow-hidden");
  });

  it("forwards the caller's sizing class to the avatar, not the dot wrapper", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", running: true },
      attrs: { class: "size-7" },
    });

    expect(wrapper.get('[data-slot="avatar"]').classes()).toContain("size-7");
    expect(wrapper.classes()).not.toContain("size-7");
  });

  // happy-dom has no layout engine, so the guard is the class contract: an
  // inline-level wrapper gets baseline-aligned, and an <img> avatar's baseline
  // is its bottom edge, which pushed image avatars above the centre of the rail
  // cell while initials avatars stayed put.
  it("keeps the wrapper out of inline baseline alignment", () => {
    const wrapper = mount(AgentAvatar, { props: { name: "Web Agent" } });

    expect(wrapper.classes()).toContain("flex");
    expect(wrapper.classes()).not.toContain("inline-flex");
  });

  it("signals a running agent with a dashed border locked to the tile edge", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", botPlatforms: ["slack"], running: true },
    });
    const avatar = wrapper.get('[data-slot="avatar"]');
    const border = wrapper.get('[data-testid="running-avatar-border"]');

    expect(avatar.attributes("data-running")).toBe("true");
    expect(border.element.tagName).toBe("SPAN");
    expect(border.find(".agent-running-border-flow").exists()).toBe(true);
    expect(avatar.element.contains(border.element)).toBe(true);
    expect(avatar.classes()).toContain("overflow-hidden");
    expect(wrapper.find('[data-testid="bot-connected-avatar"]').exists()).toBe(true);
  });

  it("keeps the running border flush with the avatar edge", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", running: true },
    });
    const border = wrapper.get('[data-testid="running-avatar-border"]');
    expect(border.classes()).toContain("agent-running-border");
    expect(agentAvatarSource).toContain("inset: -0.5px");
    expect(agentAvatarSource).toContain("padding: calc(clamp(1.5px, 5%, 2px) + 0.5px)");
    expect(agentAvatarSource).toContain("border-radius: inherit");
    expect(agentAvatarSource).not.toContain("border-radius: 4.5px");
    expect(agentAvatarSource).not.toContain("--agent-running-border-inset");
  });

  it("draws the running border without a utility ring", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", running: true },
    });
    const avatar = wrapper.get('[data-slot="avatar"]');

    const border = wrapper.get('[data-testid="running-avatar-border"]');
    expect(border.element.tagName).toBe("SPAN");
    expect(border.find(".agent-running-border-flow").exists()).toBe(true);
    expect(avatar.classes().join(" ")).not.toContain("ring-");
    expect(avatar.findAll("svg")).toHaveLength(0);
  });

  it("keeps the selected agent's solid frame instead of the running border", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", running: true, selected: true },
    });

    expect(wrapper.get('[data-slot="avatar"]').attributes("data-selected")).toBe("true");
    expect(wrapper.find('[data-testid="running-avatar-border"]').exists()).toBe(false);
  });

  it("leaves the selected treatment to the rail rather than ringing the logo", () => {
    const wrapper = mount(AgentAvatar, {
      props: { name: "Web Agent", selected: true },
    });

    expect(wrapper.get('[data-slot="avatar"]').classes().join(" ")).not.toContain("ring-");
  });
});
