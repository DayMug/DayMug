import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import AboutSettings from "./AboutSettings.vue";

vi.stubGlobal("__APP_VERSION__", "v1.2.3-test");

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      { path: "/settings/about", name: "settings-about", component: AboutSettings },
    ],
  });
  router.push("/settings/about");
  return router;
}

describe("AboutSettings", () => {
  it("displays the application name", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AboutSettings, { global: { plugins: [router] } });
    expect(wrapper.text()).toContain("DayMug");
  });

  it("displays the version from build-time constant", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AboutSettings, { global: { plugins: [router] } });
    expect(wrapper.text()).toContain("v1.2.3-test");
  });

  it("keeps the Powered by DayMug attribution required by the LICENSE", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(AboutSettings, { global: { plugins: [router] } });
    expect(wrapper.find('[data-testid="powered-by"]').exists()).toBe(true);
  });
});
