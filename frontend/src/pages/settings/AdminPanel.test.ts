import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { ref } from "vue";
import AdminPanel from "./AdminPanel.vue";

const authMe = ref<{ is_admin: boolean } | null>(null);

vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({ authMe }),
}));

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div>settings</div>" } },
      { path: "/settings/admin", name: "settings-admin", component: AdminPanel },
      {
        path: "/settings/admin/users",
        name: "settings-admin-users",
        component: { template: "<div>users</div>" },
      },
      {
        path: "/settings/admin/global",
        name: "settings-admin-global",
        component: { template: "<div>global</div>" },
      },
      {
        path: "/settings/admin/models",
        name: "settings-admin-models",
        component: { template: "<div>models</div>" },
      },
      {
        path: "/settings/admin/terminal",
        name: "settings-admin-terminal",
        component: { template: "<div>terminal</div>" },
      },
      {
        path: "/settings/usage",
        name: "settings-usage",
        component: { template: "<div>usage</div>" },
      },
    ],
  });
  router.push("/settings/admin");
  return router;
}

beforeEach(() => {
  authMe.value = null;
});

describe("AdminPanel", () => {
  it("collects every administrator capability on one page", async () => {
    authMe.value = { is_admin: true };
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AdminPanel, { global: { plugins: [router] } });

    expect(wrapper.find(".admin-panel-users").exists()).toBe(true);
    expect(wrapper.find(".admin-panel-global").exists()).toBe(true);
    expect(wrapper.find(".admin-panel-models").exists()).toBe(true);
    expect(wrapper.find(".admin-panel-terminal").exists()).toBe(true);
    expect(wrapper.find(".admin-panel-usage").exists()).toBe(true);
  });

  it("navigates to an administrator detail page", async () => {
    authMe.value = { is_admin: true };
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AdminPanel, { global: { plugins: [router] } });

    await wrapper.find(".admin-panel-users").trigger("click");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(router.currentRoute.value.name).toBe("settings-admin-users");
  });

  it("opens the model registry from its own menu row", async () => {
    authMe.value = { is_admin: true };
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AdminPanel, { global: { plugins: [router] } });

    await wrapper.find(".admin-panel-models").trigger("click");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(router.currentRoute.value.name).toBe("settings-admin-models");
  });

  it("returns a non-admin direct visitor to Settings", async () => {
    authMe.value = { is_admin: false };
    const router = makeRouter();
    await router.isReady();
    mount(AdminPanel, { global: { plugins: [router] } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(router.currentRoute.value.name).toBe("settings");
  });
});
