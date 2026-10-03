import { describe, it, expect, vi, beforeEach } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { ref } from "vue";
import SettingsHub from "./SettingsHub.vue";
import { upgradeAvailable } from "@/stores/appChromeStore";

const mockIsMobile = ref(false);
vi.mock("@/composables/useIsMobile", () => ({
  useIsMobile: () => ({ isMobile: mockIsMobile }),
}));

const mockLogout = vi.fn();
vi.mock("@/composables/useApi", async () => {
  const actual =
    await vi.importActual<typeof import("@/composables/useApi")>("@/composables/useApi");
  return {
    ...actual,
    logout: () => mockLogout(),
  };
});

interface FakeAppState {
  authMe: { value: { is_admin: boolean; username: string; name: string } | null };
  users: {
    value: {
      id: string;
      name: string;
      avatar?: string;
      bot_platforms?: string[];
    }[];
  };
  currentUser: { value: { id: string } | null };
  archivedAgents: { value: { id: string; name: string; avatar?: string }[] };
  selectUser: (id: string) => Promise<void>;
  clearAuthMe: () => void;
  loadArchivedAgents: () => Promise<void>;
  handleRestoreUser: (id: string) => Promise<void>;
}
const fakeState: FakeAppState = {
  authMe: ref(null) as FakeAppState["authMe"],
  users: ref([]) as FakeAppState["users"],
  currentUser: ref(null) as FakeAppState["currentUser"],
  archivedAgents: ref([]) as FakeAppState["archivedAgents"],
  selectUser: vi.fn().mockResolvedValue(undefined),
  clearAuthMe: vi.fn(),
  loadArchivedAgents: vi.fn().mockResolvedValue(undefined),
  handleRestoreUser: vi.fn().mockResolvedValue(undefined),
};
vi.mock("@/composables/useUsers", () => ({
  useUsers: () => fakeState,
}));
vi.mock("@/composables/useAuth", () => ({
  useAuth: () => fakeState,
}));

function makeRouter(initialPath: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: SettingsHub },
      {
        path: "/settings/system",
        name: "settings-system",
        component: { template: "<div>system</div>" },
      },
      {
        path: "/settings/account",
        name: "settings-account",
        component: { template: "<div>account</div>" },
      },
      {
        path: "/settings/about",
        name: "settings-about",
        component: { template: "<div>about</div>" },
      },
      {
        path: "/settings/crontab",
        name: "settings-crontab",
        component: { template: "<div>crontab</div>" },
      },
      {
        path: "/settings/users/add",
        name: "settings-users-add",
        component: { template: "<div>add</div>" },
      },
      {
        path: "/settings/users/:id",
        name: "settings-users-edit",
        component: { template: "<div>edit</div>" },
      },
      {
        path: "/settings/admin",
        name: "settings-admin",
        component: { template: "<div>admin-panel</div>" },
      },
      {
        path: "/settings/admin/users",
        name: "settings-admin-users",
        component: { template: "<div>admin-users</div>" },
      },
      {
        path: "/settings/admin/global",
        name: "settings-admin-global",
        component: { template: "<div>admin-global</div>" },
      },
      { path: "/", name: "home", component: { template: "<div>home</div>" } },
      { path: "/chat/:userId", name: "chat", component: { template: "<div>chat</div>" } },
      { path: "/login", name: "login", component: { template: "<div>login</div>" } },
    ],
  });
  router.push(initialPath);
  return router;
}

function resetFakeState() {
  fakeState.authMe.value = null;
  fakeState.users.value = [];
  fakeState.currentUser.value = null;
  fakeState.archivedAgents.value = [];
  vi.mocked(fakeState.selectUser).mockClear();
  vi.mocked(fakeState.clearAuthMe).mockClear();
  vi.mocked(fakeState.loadArchivedAgents).mockClear();
  vi.mocked(fakeState.handleRestoreUser).mockClear();
  mockIsMobile.value = false;
  upgradeAvailable.value = false;
}

describe("SettingsHub", () => {
  beforeEach(() => {
    resetFakeState();
    mockLogout.mockReset();
  });

  it("renders the System group with Appearance, Account, Scheduled tasks, and About rows", async () => {
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    const labels = wrapper.findAll("button").map((b) => b.text());
    expect(labels).toContain("Appearance");
    expect(labels).toContain("Account");
    expect(labels).toContain("Scheduled tasks");
    expect(labels).toContain("About");
  });

  it("builds settings groups from shadcn cards and buttons", async () => {
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });

    expect(wrapper.findAll('[data-slot="card"]').length).toBeGreaterThan(0);
    expect(wrapper.findAll('[data-slot="button"]').length).toBeGreaterThan(0);
  });

  it("lists every agent inline plus a New agent row", async () => {
    fakeState.users.value = [
      { id: "u1", name: "Researcher" },
      { id: "u2", name: "Reviewer" },
    ];
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    const text = wrapper.text();
    expect(text).toContain("Researcher");
    expect(text).toContain("Reviewer");
    expect(wrapper.find(".settings-hub-add-agent").exists()).toBe(true);
  });

  it("labels only the transport platforms attached to agents", async () => {
    fakeState.users.value = [
      { id: "u1", name: "Researcher" },
      { id: "u2", name: "Support", bot_platforms: ["slack"] },
      {
        id: "u3",
        name: "Coordinator",
        bot_platforms: ["slack", "feishu"],
      },
    ];
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    const rows = wrapper.findAll(".settings-hub-agent-row");

    expect(rows[0].text()).toContain("Researcher");
    expect(rows[0].text()).not.toContain("Slack");
    expect(rows[0].text()).not.toContain("Feishu");
    expect(rows[1].text()).toContain("Slack");
    expect(rows[1].text()).not.toContain("Bot");
    expect(rows[2].text()).toContain("Slack · Feishu (Lark)");
    expect(rows[2].text()).not.toContain("Bot");
  });

  it("hides the Archived agents group when none are archived", async () => {
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.find(".settings-hub-archived-row").exists()).toBe(false);
  });

  it("lists archived agents and restores one on click", async () => {
    fakeState.archivedAgents.value = [{ id: "a9", name: "Retired" }];
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.text()).toContain("Retired");
    const restoreBtn = wrapper.find(".settings-hub-restore");
    expect(restoreBtn.exists()).toBe(true);
    await restoreBtn.trigger("click");
    expect(fakeState.handleRestoreUser).toHaveBeenCalledWith("a9");
  });

  it("hides the Admin group when authMe.is_admin is false", async () => {
    fakeState.authMe.value = { is_admin: false, username: "alice", name: "Alice" };
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.find(".settings-hub-admin-panel").exists()).toBe(false);
    expect(wrapper.text()).toContain("Usage");
  });

  it("does not expose the Admin Panel from the settings hub", async () => {
    fakeState.authMe.value = { is_admin: true, username: "alice", name: "Alice" };
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.find(".settings-hub-admin-panel").exists()).toBe(false);
    expect(wrapper.find(".settings-hub-admin-users").exists()).toBe(false);
    expect(wrapper.findAll("button").some((button) => button.text() === "Usage")).toBe(false);
  });

  it("opens the Admin Panel from the hub on mobile, where there is no sidebar", async () => {
    fakeState.authMe.value = { is_admin: true, username: "alice", name: "Alice" };
    mockIsMobile.value = true;
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    const row = wrapper.find(".settings-hub-admin-panel");
    expect(row.exists()).toBe(true);
    await row.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("settings-admin");
  });

  it("keeps the Admin Panel row from non-admins on mobile", async () => {
    fakeState.authMe.value = { is_admin: false, username: "bob", name: "Bob" };
    mockIsMobile.value = true;
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.find(".settings-hub-admin-panel").exists()).toBe(false);
  });

  it("marks the mobile Admin Panel row when an upgrade is available", async () => {
    fakeState.authMe.value = { is_admin: true, username: "alice", name: "Alice" };
    mockIsMobile.value = true;
    upgradeAvailable.value = true;
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    expect(wrapper.find('[data-testid="settings-hub-admin-upgrade-badge"]').exists()).toBe(true);
  });

  it("navigates to the agent's edit page after priming the chosen user", async () => {
    fakeState.users.value = [{ id: "u1", name: "Researcher" }];
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    const agentBtn = wrapper.findAll("button").find((b) => b.text().includes("Researcher"));
    expect(agentBtn).toBeTruthy();
    await agentBtn!.trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(fakeState.selectUser).toHaveBeenCalledWith("u1");
    expect(router.currentRoute.value.name).toBe("settings-users-edit");
    expect(router.currentRoute.value.params.id).toBe("u1");
  });

  it("returns to chat for the current user when the back button is clicked", async () => {
    fakeState.currentUser.value = { id: "u1" };
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    await wrapper.find(".settings-hub-back").trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.name).toBe("chat");
    expect(router.currentRoute.value.params.userId).toBe("u1");
  });

  it("falls back to / when no current user is set", async () => {
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    await wrapper.find(".settings-hub-back").trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.path).toBe("/");
  });

  it("logs out and routes to /login when the sign-out button is clicked", async () => {
    mockLogout.mockResolvedValueOnce(undefined);
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    await wrapper.find(".settings-hub-signout").trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(mockLogout).toHaveBeenCalledTimes(1);
    expect(fakeState.clearAuthMe).toHaveBeenCalledTimes(1);
    expect(router.currentRoute.value.name).toBe("login");
  });

  it("still routes to /login when the logout API call fails", async () => {
    // Server may have already invalidated the session; local state is
    // cleared and the user lands on the login page anyway.
    mockLogout.mockRejectedValueOnce(new Error("network down"));
    const router = makeRouter("/settings");
    await router.isReady();
    const wrapper = mount(SettingsHub, { global: { plugins: [router] } });
    await wrapper.find(".settings-hub-signout").trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.name).toBe("login");
  });
});
