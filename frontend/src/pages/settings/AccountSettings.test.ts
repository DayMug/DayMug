import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import AccountSettings from "./AccountSettings.vue";

const mockChangePassword = vi.fn();
vi.mock("@/composables/useApi", () => ({
  changeMyPassword: (...args: unknown[]) => mockChangePassword(...args),
}));

beforeEach(() => {
  mockChangePassword.mockReset();
});

async function flush() {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      { path: "/settings/account", name: "settings-account", component: AccountSettings },
    ],
  });
  router.push("/settings/account");
  return router;
}

function mountAccount() {
  const router = makeRouter();
  return mount(AccountSettings, { global: { plugins: [router] } });
}

describe("AccountSettings", () => {
  it("submits current+new password to the API on success", async () => {
    mockChangePassword.mockResolvedValueOnce(undefined);
    const wrapper = mountAccount();
    const inputs = wrapper.findAll("input[type=password]");
    await inputs[0].setValue("oldsecret");
    await inputs[1].setValue("newsecret");
    await inputs[2].setValue("newsecret");
    await wrapper.find("form").trigger("submit");
    await flush();
    expect(mockChangePassword).toHaveBeenCalledWith("oldsecret", "newsecret");
    expect(wrapper.text()).toContain("Password updated");
  });

  it("requires confirm password to match the new password", async () => {
    const wrapper = mountAccount();
    const inputs = wrapper.findAll("input[type=password]");
    await inputs[0].setValue("oldsecret");
    await inputs[1].setValue("newsecret");
    await inputs[2].setValue("different");
    await wrapper.find("form").trigger("submit");
    await flush();
    expect(mockChangePassword).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("New password and confirmation do not match");
  });

  it("rejects when new password equals current password", async () => {
    const wrapper = mountAccount();
    const inputs = wrapper.findAll("input[type=password]");
    await inputs[0].setValue("samesecret");
    await inputs[1].setValue("samesecret");
    await inputs[2].setValue("samesecret");
    await wrapper.find("form").trigger("submit");
    await flush();
    expect(mockChangePassword).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("New password must differ from the current password");
  });

  it("surfaces the API error message on failure", async () => {
    mockChangePassword.mockRejectedValueOnce(new Error("current password is incorrect"));
    const wrapper = mountAccount();
    const inputs = wrapper.findAll("input[type=password]");
    await inputs[0].setValue("wrong");
    await inputs[1].setValue("newsecret");
    await inputs[2].setValue("newsecret");
    await wrapper.find("form").trigger("submit");
    await flush();
    expect(mockChangePassword).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("current password is incorrect");
  });
});
