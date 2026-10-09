import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import NotificationsSettings from "./NotificationsSettings.vue";

const mockFetchMe = vi.fn();
const mockUpdate = vi.fn();
vi.mock("@/composables/useApi", () => ({
  fetchMe: () => mockFetchMe(),
  updateMyNotifications: (...args: unknown[]) => mockUpdate(...args),
}));

const mockLoadAuthMe = vi.fn();
vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({
    authMe: { value: null },
    loadAuthMe: mockLoadAuthMe,
  }),
}));

beforeEach(() => {
  mockFetchMe.mockReset();
  mockUpdate.mockReset();
  mockLoadAuthMe.mockReset();
  mockLoadAuthMe.mockResolvedValue(null);
});

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      {
        path: "/settings/notifications",
        name: "settings-notifications",
        component: NotificationsSettings,
      },
    ],
  });
  router.push("/settings/notifications");
  return router;
}

async function mountPage() {
  const router = makeRouter();
  await router.isReady();
  const wrapper = mount(NotificationsSettings, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe("NotificationsSettings", () => {
  it("hydrates Bark + PushDeer + channel from /auth/me on mount", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "https://api.day.app/abc",
      pushdeer_key: "PDU-key",
      notification_channel: "pushdeer",
    });
    const wrapper = await mountPage();
    const bark = wrapper.find("input#notifications-bark-url").element as HTMLInputElement;
    const pd = wrapper.find("input#notifications-pushdeer-key").element as HTMLInputElement;
    expect(bark.value).toBe("https://api.day.app/abc");
    expect(pd.value).toBe("PDU-key");
    // Both channels populated → selector visible and pushdeer is checked.
    const pushdeerRadio = wrapper.find('input[type="radio"][value="pushdeer"]')
      .element as HTMLInputElement;
    expect(pushdeerRadio.checked).toBe(true);
  });

  it("submits trimmed Bark URL + PushDeer key and the chosen channel", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "",
      pushdeer_key: "",
      notification_channel: "",
    });
    mockUpdate.mockResolvedValueOnce(undefined);
    const wrapper = await mountPage();

    await wrapper.find("input#notifications-bark-url").setValue("  https://api.day.app/new  ");
    await wrapper.find("input#notifications-pushdeer-key").setValue("  PDU-new  ");
    await flushPromises();
    // Both fields populated → radio appears; pick pushdeer.
    await wrapper.find('input[type="radio"][value="pushdeer"]').trigger("change");
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    expect(mockUpdate).toHaveBeenCalledWith({
      bark_url: "https://api.day.app/new",
      pushdeer_key: "PDU-new",
      notification_channel: "pushdeer",
    });
    expect(wrapper.text()).toContain("Notification settings updated");
  });

  it("hides the channel selector when only one channel is configured", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "https://api.day.app/abc",
      pushdeer_key: "",
      notification_channel: "",
    });
    const wrapper = await mountPage();
    expect(wrapper.find('input[type="radio"][value="pushdeer"]').exists()).toBe(false);
  });

  it("clears notification_channel when only one channel is configured on submit", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "https://api.day.app/abc",
      // Stale channel preference from a previous "both configured" state.
      pushdeer_key: "",
      notification_channel: "pushdeer",
    });
    mockUpdate.mockResolvedValueOnce(undefined);
    const wrapper = await mountPage();
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(mockUpdate).toHaveBeenCalledWith({
      bark_url: "https://api.day.app/abc",
      pushdeer_key: "",
      notification_channel: "",
    });
  });

  it("renders external links to the Bark and PushDeer official sites", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "",
      pushdeer_key: "",
      notification_channel: "",
    });
    const wrapper = await mountPage();
    const hrefs = wrapper.findAll("a").map((a) => a.attributes("href"));
    expect(hrefs).toContain("https://bark.day.app/#/");
    expect(hrefs).toContain("https://www.pushdeer.com/");
  });

  it("surfaces the API error message when the save fails", async () => {
    mockFetchMe.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "Alice",
      is_admin: false,
      bark_url: "",
      pushdeer_key: "",
      notification_channel: "",
    });
    mockUpdate.mockRejectedValueOnce(new Error("upstream is down"));
    const wrapper = await mountPage();

    await wrapper.find("input#notifications-bark-url").setValue("https://api.day.app/x");
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    expect(wrapper.text()).toContain("upstream is down");
  });
});
