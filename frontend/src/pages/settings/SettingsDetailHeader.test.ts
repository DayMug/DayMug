import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      {
        path: "/settings/admin",
        name: "settings-admin",
        component: { template: "<div/>" },
      },
      {
        path: "/settings/system",
        name: "settings-system",
        component: { template: "<div/>" },
      },
    ],
  });
  router.push("/settings/system");
  return router;
}

describe("SettingsDetailHeader", () => {
  it("renders the title and optional eyebrow", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SettingsDetailHeader, {
      props: { title: "Appearance", eyebrow: "System" },
      global: { plugins: [router] },
    });
    expect(wrapper.find("h1").text()).toBe("Appearance");
    expect(wrapper.text()).toContain("System");
  });

  it("navigates back to the settings hub on back-arrow click", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SettingsDetailHeader, {
      props: { title: "Appearance" },
      global: { plugins: [router] },
    });
    await wrapper.find(".settings-detail-back").trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.name).toBe("settings");
  });

  it("returns to the administrator panel when that is the parent", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SettingsDetailHeader, {
      props: { title: "Users", parentRouteName: "settings-admin" },
      global: { plugins: [router] },
    });
    await wrapper.find(".settings-detail-back").trigger("click");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(router.currentRoute.value.name).toBe("settings-admin");
  });

  it("goes to the parent menu even when history came from elsewhere", async () => {
    const router = makeRouter();
    await router.isReady();
    await router.push("/settings/admin");
    await router.push("/settings/system");
    const state = window.history.state;
    window.history.replaceState({ back: "/settings/admin" }, "");
    try {
      const wrapper = mount(SettingsDetailHeader, {
        props: { title: "Appearance" },
        global: { plugins: [router] },
      });
      await wrapper.find(".settings-detail-back").trigger("click");
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(router.currentRoute.value.name).toBe("settings");
    } finally {
      window.history.replaceState(state, "");
    }
  });

  it("exposes an actions slot to the right of the title", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SettingsDetailHeader, {
      props: { title: "Edit user" },
      slots: { actions: '<button class="slot-btn">Save</button>' },
      global: { plugins: [router] },
    });
    expect(wrapper.find(".slot-btn").exists()).toBe(true);
  });
});
