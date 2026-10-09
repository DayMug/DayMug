import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import UserContextMenu from "./UserContextMenu.vue";

const user = {
  id: "u1abcdef",
  name: "Alice",
  username: "",
  email: "",
  is_admin: false,
  disabled: false,
  work_dir: "/home/alice",
  avatar: "A",
  role_definition: "",
  mcp_config: "",
  claude_md_content: "",
  manage_claude_md: false,
  case_mode: false,
  sandbox_mode: "jailed" as const,
  sort_order: 0,
  archived: false,
  bark_url: "",
  pushdeer_key: "",
  notification_channel: "",
  created_at: "",
};

describe("UserContextMenu", () => {
  it("renders avatar, name, and short agent id in the header", () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    expect(wrapper.find(".ctx-avatar").text()).toBe("A");
    expect(wrapper.find(".ctx-name").text()).toBe("Alice");
    // agt_<first 4 chars of id> per UI design's metadata pill
    expect(wrapper.text()).toContain("agt_u1ab");
  });

  it("offers Edit, stale cleanup, and Archive", () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    expect(wrapper.findAll(".ctx-btn")).toHaveLength(3);
    expect(wrapper.find(".ctx-btn.edit").text()).toContain("Edit agent");
    expect(wrapper.find(".ctx-btn.cleanup").text()).toContain("older than 14 days");
    expect(wrapper.find(".ctx-btn.archive").text()).toContain("Archive");
  });

  // The trimmed menu is icon + label only — no shortcut hints to keep in
  // sync with bindings that don't exist.
  it("renders no keyboard shortcut hints", () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    expect(wrapper.find("kbd").exists()).toBe(false);
  });

  it("emits archive when Archive is clicked", async () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    await wrapper.find(".ctx-btn.archive").trigger("click");
    expect(wrapper.emitted("archive")).toBeTruthy();
    expect(wrapper.emitted("archive")![0]).toEqual([user]);
  });

  it("does not include a destructive Delete item (handled in settings)", () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    expect(wrapper.find(".ctx-btn.delete").exists()).toBe(false);
  });

  it("emits edit when Edit is clicked", async () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    await wrapper.find(".ctx-btn.edit").trigger("click");
    expect(wrapper.emitted("edit")).toBeTruthy();
    expect(wrapper.emitted("edit")![0]).toEqual([user]);
  });

  it("emits cleanup when stale cleanup is clicked", async () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
    });
    await wrapper.find(".ctx-btn.cleanup").trigger("click");
    expect(wrapper.emitted("cleanup")![0]).toEqual([user]);
  });

  it("emits close on outside click", async () => {
    const wrapper = mount(UserContextMenu, {
      props: { user, x: 100, y: 200 },
      attachTo: document.body,
    });
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    expect(wrapper.emitted("close")).toBeTruthy();
    wrapper.unmount();
  });
});
