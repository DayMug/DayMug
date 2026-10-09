import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import LoginPage from "./LoginPage.vue";

const mockLogin = vi.fn();
const mockFetchAuthOptions = vi.fn();
const mockFetchBootstrapStatus = vi.fn();

vi.mock("@/composables/useApi", () => ({
  login: (...args: unknown[]) => mockLogin(...args),
  fetchAuthOptions: () => mockFetchAuthOptions(),
  fetchBootstrapStatus: () => mockFetchBootstrapStatus(),
}));

const assignMock = vi.fn();
vi.stubGlobal("location", {
  assign: assignMock,
  origin: "http://localhost",
} as unknown as Location);

function makeRouter(initialPath = "/login") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { template: "<div/>" } },
      { path: "/login", name: "login", component: LoginPage },
      { path: "/setup/admin", name: "setup-admin", component: { template: "<div/>" } },
    ],
  });
  router.push(initialPath);
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  // Default: password login enabled, OIDC off — matches the typical config.
  mockFetchAuthOptions.mockResolvedValue({
    password_login_enabled: true,
    oidc: { enabled: false, button_label: "" },
  });
  // Default to a populated install so the login form renders. The
  // first-run redirect is exercised explicitly below.
  mockFetchBootstrapStatus.mockResolvedValue({ has_users: true, setup_required: false });
});

describe("LoginPage", () => {
  it("renders the login form", async () => {
    // The default locale in tests is English (see src/test/setup.ts).
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    expect(wrapper.findAll("input").length).toBe(2);
    const submit = wrapper.find('button[type="submit"]');
    expect(submit.exists()).toBe(true);
    expect(submit.text()).toContain("Enter DayMug");
  });

  it("keeps the Powered by DayMug attribution required by the LICENSE", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    expect(wrapper.find('[data-testid="powered-by"]').exists()).toBe(true);
  });

  it("uses shadcn primitives for the authentication surface", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    expect(wrapper.find('[data-slot="card"]').exists()).toBe(true);
    expect(wrapper.find('[data-slot="button"][type="submit"]').exists()).toBe(true);
  });

  it("shows validation error when fields are empty", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();
    expect(wrapper.text()).toContain("Please enter your username and password");
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it("routes an admin to Settings on success", async () => {
    mockLogin.mockResolvedValueOnce({
      id: "u1",
      username: "admin",
      name: "Admin",
      is_admin: true,
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("hunter2");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(mockLogin).toHaveBeenCalledWith("admin", "hunter2");
    expect(assignMock).toHaveBeenCalledWith("/settings");
  });

  it("keeps the regular-user default landing on the chat surface", async () => {
    mockLogin.mockResolvedValueOnce({
      id: "u2",
      username: "alice",
      name: "Alice",
      is_admin: false,
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("alice");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(assignMock).toHaveBeenCalledWith("/");
  });

  it("uses the redirect query param after login", async () => {
    mockLogin.mockResolvedValueOnce({
      id: "u1",
      username: "admin",
      name: "Admin",
      is_admin: true,
    });
    const router = makeRouter("/login?redirect=/chat/u1");
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(assignMock).toHaveBeenCalledWith("/chat/u1");
  });

  it("ignores a redirect that points back at /login", async () => {
    mockLogin.mockResolvedValueOnce({ id: "u1", username: "admin", name: "Admin" });
    const router = makeRouter("/login?redirect=/login");
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(assignMock).toHaveBeenCalledWith("/");
  });

  // The ?redirect value is attacker-controlled, so a crafted login link must
  // not be able to bounce a freshly authenticated user off-origin.
  it.each([
    ["//evil.com"],
    ["/\\evil.com"],
    ["https://evil.com"],
    ["///evil.com"],
    ["  //evil.com"],
  ])("refuses to redirect off-origin for %s", async (hostile) => {
    mockLogin.mockResolvedValueOnce({ id: "u1", username: "admin", name: "Admin" });
    const router = makeRouter(`/login?redirect=${encodeURIComponent(hostile)}`);
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(assignMock).toHaveBeenCalledWith("/");
  });

  it("keeps a same-origin redirect with query and hash intact", async () => {
    mockLogin.mockResolvedValueOnce({ id: "u1", username: "admin", name: "Admin" });
    const router = makeRouter(
      `/login?redirect=${encodeURIComponent("/settings/users?tab=agents#top")}`,
    );
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(assignMock).toHaveBeenCalledWith("/settings/users?tab=agents#top");
  });

  it("shows credential error on 401", async () => {
    const err = Object.assign(new Error("invalid credentials"), { status: 401 });
    mockLogin.mockRejectedValueOnce(err);
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("wrong");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(wrapper.text()).toContain("Incorrect username or password");
    expect(assignMock).not.toHaveBeenCalled();
  });

  it("renders the SSO button when oidc is enabled", async () => {
    mockFetchAuthOptions.mockResolvedValueOnce({
      password_login_enabled: true,
      oidc: { enabled: true, button_label: "Sign in with Casdoor" },
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(wrapper.text()).toContain("Sign in with Casdoor");
    // Form is still there because password_login_enabled=true.
    expect(wrapper.find("form").exists()).toBe(true);
  });

  it("hides the password form when password_login_enabled is false", async () => {
    mockFetchAuthOptions.mockResolvedValueOnce({
      password_login_enabled: false,
      oidc: { enabled: true, button_label: "SSO" },
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(wrapper.find("form").exists()).toBe(false);
    expect(wrapper.text()).toContain("SSO");
  });

  it("surfaces ?oidc_error= from the callback redirect", async () => {
    const router = makeRouter("/login?oidc_error=access%20denied");
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(wrapper.text()).toContain("access denied");
  });

  it("starts OIDC by navigating to /api/auth/oidc/login", async () => {
    mockFetchAuthOptions.mockResolvedValueOnce({
      password_login_enabled: true,
      oidc: { enabled: true, button_label: "SSO" },
    });
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    const ssoButton = wrapper.findAll("button").find((b) => b.text().includes("SSO"));
    expect(ssoButton).toBeDefined();
    await ssoButton!.trigger("click");
    expect(assignMock).toHaveBeenCalledWith("/api/auth/oidc/login");
  });

  it("renders a language switcher in the top corner with EN selected by default", async () => {
    // The login page is the very first surface a user sees, so the language
    // toggle has to be reachable before they log in. Assert that both options
    // are present and the EN option is the active one (test setup pins the
    // initial locale to English).
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    const buttons = wrapper.findAll(".language-switcher button");
    expect(buttons.length).toBe(2);
    const en = buttons.find((b) => b.attributes("data-locale") === "en");
    const zh = buttons.find((b) => b.attributes("data-locale") === "zh");
    expect(en?.attributes("aria-pressed")).toBe("true");
    expect(zh?.attributes("aria-pressed")).toBe("false");
  });

  it("switches the page text to Chinese when 中 is clicked", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    const zh = wrapper
      .findAll(".language-switcher button")
      .find((b) => b.attributes("data-locale") === "zh");
    expect(zh).toBeDefined();
    await zh!.trigger("click");
    await flushPromises();
    const submit = wrapper.find('button[type="submit"]');
    expect(submit.text()).toContain("登录");
  });

  it("renders a workbench preview that explains the product", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    const preview = wrapper.find('[data-testid="workbench-preview"]');
    expect(preview.exists()).toBe(true);
    expect(preview.text()).toContain("Today’s workbench");
    expect(preview.text()).toContain("Claude");
    expect(preview.text()).toContain("@Contract bot");
    expect(preview.text()).toContain("Codex");
    expect(preview.text()).toContain("3 tasks");
  });

  it("uses accurate Chinese product copy", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });
    const zh = wrapper
      .findAll(".language-switcher button")
      .find((b) => b.attributes("data-locale") === "zh");
    await zh!.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("交给整个团队");
    expect(wrapper.text()).toContain("装在你自己的服务器上");
    expect(wrapper.text()).not.toContain("本机");
    expect(wrapper.text()).toContain("登录 DayMug");
    expect(wrapper.text()).not.toContain("平静网页家");
  });

  it("redirects to /setup/admin when the install has no users yet", async () => {
    // First-run gate: a brand-new install reports setup_required=true.
    // LoginPage should bounce the visitor to /setup/admin on mount instead of
    // showing a password form they can't satisfy.
    mockFetchBootstrapStatus.mockResolvedValueOnce({ has_users: false, setup_required: true });
    const router = makeRouter();
    await router.isReady();
    const replaceSpy = vi.spyOn(router, "replace");
    mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(replaceSpy).toHaveBeenCalledWith({ name: "setup-admin" });
  });

  it("stays on /login when SSO provisions the first admin", async () => {
    mockFetchBootstrapStatus.mockResolvedValueOnce({ has_users: false, setup_required: false });
    const router = makeRouter();
    await router.isReady();
    const replaceSpy = vi.spyOn(router, "replace");
    mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(replaceSpy).not.toHaveBeenCalled();
  });

  it("stays on /login when the install already has users", async () => {
    mockFetchBootstrapStatus.mockResolvedValueOnce({ has_users: true, setup_required: false });
    const router = makeRouter();
    await router.isReady();
    const replaceSpy = vi.spyOn(router, "replace");
    mount(LoginPage, { global: { plugins: [router] } });
    await flushPromises();
    expect(replaceSpy).not.toHaveBeenCalled();
  });

  it("shows generic error on network failure", async () => {
    mockLogin.mockRejectedValueOnce(new Error("boom"));
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(LoginPage, { global: { plugins: [router] } });

    const inputs = wrapper.findAll("input");
    await inputs[0].setValue("admin");
    await inputs[1].setValue("pw");
    await wrapper.find("form").trigger("submit.prevent");
    await flushPromises();

    expect(wrapper.text()).toContain("boom");
  });
});
