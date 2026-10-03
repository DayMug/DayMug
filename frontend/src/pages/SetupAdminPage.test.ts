import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import SetupAdminPage from "./SetupAdminPage.vue";

const mockFetchBootstrapStatus = vi.fn();
const mockCreateBootstrapAdmin = vi.fn();

vi.mock("@/composables/useApi", () => ({
  fetchBootstrapStatus: () => mockFetchBootstrapStatus(),
  createBootstrapAdmin: (...args: unknown[]) => mockCreateBootstrapAdmin(...args),
}));

const assignMock = vi.fn();
vi.stubGlobal("location", {
  assign: assignMock,
  origin: "http://localhost",
} as unknown as Location);

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { template: "<div/>" } },
      { path: "/login", name: "login", component: { template: "<div/>" } },
      { path: "/setup/admin", name: "setup-admin", component: SetupAdminPage },
    ],
  });
  router.push("/setup/admin");
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchBootstrapStatus.mockResolvedValue({
    has_users: false,
    setup_required: true,
    setup_mode: "password",
    min_password_length: 8,
  });
});

describe("SetupAdminPage", () => {
  it("renders the setup form once it confirms the install is fresh", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    // Email, username, password, confirm password — four inputs.
    expect(wrapper.findAll("input").length).toBe(4);
    expect(wrapper.find('button[type="submit"]').exists()).toBe(true);
  });

  it("redirects to /login when an admin already exists", async () => {
    // Anyone who navigates here on a populated install should be bounced
    // back to the normal login surface. The bootstrap endpoint would also
    // 409 the submission, but the redirect saves the user from typing
    // into a doomed form.
    mockFetchBootstrapStatus.mockResolvedValueOnce({ has_users: true, setup_required: false });
    const router = makeRouter();
    await router.isReady();
    const replaceSpy = vi.spyOn(router, "replace");
    mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(replaceSpy).toHaveBeenCalledWith({ name: "login" });
  });

  it("redirects to /login when SSO provisions the first admin", async () => {
    mockFetchBootstrapStatus.mockResolvedValueOnce({ has_users: false, setup_required: false });
    const router = makeRouter();
    await router.isReady();
    const replaceSpy = vi.spyOn(router, "replace");
    mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(replaceSpy).toHaveBeenCalledWith({ name: "login" });
  });

  it("auto-fills the username from the email local-part", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    const inputs = wrapper.findAll("input");
    // First input is email; second is username.
    await inputs[0].setValue("alice@example.com");
    expect((inputs[1].element as HTMLInputElement).value).toBe("alice");
  });

  it("submits and hard-reloads to the providers page on success", async () => {
    mockCreateBootstrapAdmin.mockResolvedValueOnce({
      id: "u1",
      username: "alice",
      name: "alice",
      is_admin: true,
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("alice@example.com");
    await inputs[2].setValue("hunter2hunter2");
    await inputs[3].setValue("hunter2hunter2");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(mockCreateBootstrapAdmin).toHaveBeenCalledWith({
      username: "alice",
      email: "alice@example.com",
      password: "hunter2hunter2",
    });
    expect(assignMock).toHaveBeenCalledWith("/settings/admin/models");
  });

  it("shows an error when the username does not match the email local-part", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("alice@example.com");
    // Manually break the invariant.
    await inputs[1].setValue("not-alice");
    await inputs[2].setValue("hunter2hunter2");
    await inputs[3].setValue("hunter2hunter2");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).not.toHaveBeenCalled();
    // Error text rendered (English default locale in tests).
    expect(wrapper.text().toLowerCase()).toContain("username");
  });

  it("shows an error when fields are empty", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).not.toHaveBeenCalled();
  });

  async function mountReady() {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(SetupAdminPage, { global: { plugins: [router] } });
    await flushPromises();
    return wrapper;
  }

  it("rejects a password shorter than the server minimum", async () => {
    const wrapper = await mountReady();
    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("alice@example.com");
    await inputs[2].setValue("short");
    await inputs[3].setValue("short");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).not.toHaveBeenCalled();
    expect(wrapper.find('[role="alert"]').text()).toContain("8");
  });

  it("rejects mismatched password confirmation", async () => {
    const wrapper = await mountReady();
    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("alice@example.com");
    await inputs[2].setValue("hunter2hunter2");
    await inputs[3].setValue("hunter2hunter3");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).not.toHaveBeenCalled();
    expect(wrapper.find('[role="alert"]').exists()).toBe(true);
  });

  it("rejects an email local-part that cannot name a home directory", async () => {
    const wrapper = await mountReady();
    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("../escape@example.com");
    await inputs[2].setValue("hunter2hunter2");
    await inputs[3].setValue("hunter2hunter2");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).not.toHaveBeenCalled();
    expect(wrapper.find('[role="alert"]').exists()).toBe(true);
  });

  it("asks only for the email when password sign-in is off (sso_email)", async () => {
    mockFetchBootstrapStatus.mockResolvedValueOnce({
      has_users: false,
      setup_required: true,
      setup_mode: "sso_email",
      min_password_length: 8,
    });
    mockCreateBootstrapAdmin.mockResolvedValueOnce({ id: "u1", username: "alice" });
    const wrapper = await mountReady();
    expect(wrapper.find('[data-testid="setup-sso-hint"]').exists()).toBe(true);
    const inputs = wrapper.findAll("input");
    expect(inputs.length).toBe(2);
    await inputs[0].setValue("alice@example.com");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(mockCreateBootstrapAdmin).toHaveBeenCalledWith({
      username: "alice",
      email: "alice@example.com",
    });
    expect(assignMock).toHaveBeenCalledWith("/settings/admin/models");
  });

  it("shows the config fix instead of a form when no sign-in method is enabled", async () => {
    mockFetchBootstrapStatus.mockResolvedValueOnce({
      has_users: false,
      setup_required: true,
      setup_mode: "unavailable",
      min_password_length: 8,
    });
    const wrapper = await mountReady();
    expect(wrapper.find('[data-testid="setup-unavailable"]').exists()).toBe(true);
    expect(wrapper.find("form").exists()).toBe(false);
    expect(wrapper.text()).toContain("password_login_enabled");
  });
});
