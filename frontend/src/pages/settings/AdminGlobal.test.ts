import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import AdminGlobal from "./AdminGlobal.vue";

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      { path: "/settings/admin/global", name: "settings-admin-global", component: AdminGlobal },
    ],
  });
  router.push("/settings/admin/global");
  return router;
}

function mountAdmin() {
  const router = makeRouter();
  return mount(AdminGlobal, { global: { plugins: [router] } });
}

const mockFetchPublicConfig = vi.fn();
const mockUpgradeCheck = vi.fn();
const mockUpgradeApply = vi.fn();
const mockUpgradeRestart = vi.fn();
const mockUpgradeBusy = vi.fn();
const mockUpgradeStatus = vi.fn();
const mockOptimizeDatabase = vi.fn();
const mockDatabaseSize = vi.fn();
const mockFetchPricing = vi.fn();
const mockSavePricing = vi.fn();
const mockFetchHelpDoc = vi.fn();
const mockSaveHelpDoc = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminFetchPublicConfig: (...args: unknown[]) => mockFetchPublicConfig(...args),
  adminUpgradeCheck: (...args: unknown[]) => mockUpgradeCheck(...args),
  adminUpgradeApply: (...args: unknown[]) => mockUpgradeApply(...args),
  adminUpgradeRestart: (...args: unknown[]) => mockUpgradeRestart(...args),
  adminUpgradeBusy: (...args: unknown[]) => mockUpgradeBusy(...args),
  adminUpgradeStatus: (...args: unknown[]) => mockUpgradeStatus(...args),
  adminOptimizeDatabase: (...args: unknown[]) => mockOptimizeDatabase(...args),
  adminDatabaseSize: (...args: unknown[]) => mockDatabaseSize(...args),
  adminFetchPricing: (...args: unknown[]) => mockFetchPricing(...args),
  adminSavePricing: (...args: unknown[]) => mockSavePricing(...args),
  fetchHelpDoc: (...args: unknown[]) => mockFetchHelpDoc(...args),
  adminSaveHelpDoc: (...args: unknown[]) => mockSaveHelpDoc(...args),
}));

const mockConfirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));

const baseConfig = {
  default_home_root: "/srv/users",
  providers: [{ name: "default", type: "claude", max_concurrent: 2 }],
  sandbox: { enabled: false, type: "noop" },
  upgrade: { enabled: true },
  current_version: "v1.0.0",
  backend: "sqlite",
};

const basePricing = {
  provider: "codex",
  models: ["gpt-5"],
  rates: { "gpt-5": { input: 1, output: 2 } },
  defaults: { "gpt-5": { input: 1, output: 2 } },
};

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchPublicConfig.mockResolvedValue(baseConfig);
  mockDatabaseSize.mockResolvedValue(2 * 1024 * 1024);
  mockFetchPricing.mockResolvedValue(basePricing);
  mockFetchHelpDoc.mockResolvedValue({ markdown: "" });
  mockUpgradeBusy.mockResolvedValue({ in_flight_jobs: 0, jobs: [] });
  mockUpgradeRestart.mockResolvedValue({ status: "restarting", in_flight_jobs: 0, jobs: [] });
  mockUpgradeStatus.mockResolvedValue({
    phase: "idle",
    old_version: "",
    new_version: "",
    started_at: "",
    finished_at: "",
    error: "",
    current_version: "v1.0.0",
    backup_available: false,
  });
});

describe("AdminGlobal upgrade panel", () => {
  it("shows the current running version", async () => {
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("v1.0.0");
  });

  it("hides upgrade buttons when feature is disabled", async () => {
    mockFetchPublicConfig.mockResolvedValueOnce({
      ...baseConfig,
      upgrade: { enabled: false },
    });
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("Self-upgrade is not configured");
    expect(wrapper.text()).not.toContain("Check for updates");
  });

  it("renders Update button after a check finds a new version", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "2026-04-30T00:00:00Z",
      has_update: true,
      download_url: "https://example.com/bin",
      in_flight_jobs: 0,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("Graceful upgrade to v1.1.0");
    expect(wrapper.text()).toContain("Graceful restart");
  });

  it("requests a service restart after confirmation", async () => {
    mockConfirm.mockResolvedValue(true);
    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Graceful restart"))
      ?.trigger("click");
    await flushPromises();

    expect(mockUpgradeRestart).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain("Restart requested");
  });

  it("can schedule a graceful restart while conversations are running", async () => {
    mockUpgradeBusy.mockResolvedValueOnce({
      in_flight_jobs: 1,
      jobs: [
        { id: 1, username: "alice", account_name: "default", started_at: "2026-01-01T00:00:00Z" },
      ],
    });
    mockUpgradeRestart.mockResolvedValueOnce({
      status: "waiting_for_conversations",
      in_flight_jobs: 1,
      jobs: [
        { id: 1, username: "alice", account_name: "default", started_at: "2026-01-01T00:00:00Z" },
      ],
    });
    mockConfirm.mockResolvedValue(true);
    const wrapper = mountAdmin();
    await flushPromises();

    const restartButton = wrapper
      .findAll("button")
      .find((b) => b.text().includes("Graceful restart"));
    expect(restartButton?.attributes("disabled")).toBeUndefined();

    await restartButton?.trigger("click");
    await flushPromises();

    expect(mockUpgradeRestart).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain("Graceful restart scheduled");
  });

  it("renders recent releases with commit subjects after a check", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.2.0",
      released_at: "2026-05-22T00:00:00Z",
      has_update: true,
      download_url: "https://example.com/bin",
      in_flight_jobs: 0,
      recent_releases: [
        {
          version: "v1.2.0",
          released_at: "2026-05-22T00:00:00Z",
          notes: [
            { hash: "abc1234", subject: "feat: add admin upgrade changelog" },
            { hash: "def5678", subject: "fix: race in upgrader" },
          ],
        },
        {
          version: "v1.1.0",
          released_at: "2026-04-30T00:00:00Z",
          notes: [],
        },
      ],
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();

    const panel = wrapper.find('[data-testid="recent-releases"]');
    expect(panel.exists()).toBe(true);
    const text = panel.text();
    expect(text).toContain("v1.2.0");
    expect(text).toContain("feat: add admin upgrade changelog");
    expect(text).toContain("abc1234");
    expect(text).toContain("v1.1.0");
    // v1.1.0 has no non-filtered commits → fallback copy.
    expect(text).toContain("General improvements");
  });

  it("hides the recent releases panel when the manifest doesn't supply any", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "https://example.com/bin",
      in_flight_jobs: 0,
      recent_releases: [],
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="recent-releases"]').exists()).toBe(false);
  });

  it("reports already-up-to-date when has_update is false", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.0.0",
      released_at: "",
      has_update: false,
      download_url: "",
      in_flight_jobs: 0,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("Already on the latest version");
  });

  it("surfaces a successful previous upgrade in the status panel", async () => {
    mockUpgradeStatus.mockReset();
    mockUpgradeStatus.mockResolvedValue({
      phase: "ok",
      old_version: "v0.9.0",
      new_version: "v1.0.0",
      started_at: "",
      finished_at: "2026-04-30T00:00:00Z",
      error: "",
      current_version: "v1.0.0",
      backup_available: true,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).not.toContain("Rollback");
    expect(wrapper.text()).toContain("Last upgrade succeeded");
  });

  it("surfaces a previous rolled_back upgrade in the status panel", async () => {
    mockUpgradeStatus.mockReset();
    mockUpgradeStatus.mockResolvedValue({
      phase: "rolled_back",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      started_at: "",
      finished_at: "2026-04-30T00:00:00Z",
      error: "health probe failed",
      current_version: "v1.0.0",
      backup_available: true,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("Last upgrade was rolled back");
  });

  it("shows the busy banner and disables Update when conversations are running", async () => {
    mockUpgradeBusy.mockResolvedValue({
      in_flight_jobs: 2,
      jobs: [
        {
          id: 1,
          username: "alice",
          provider_type: "codex",
          account_name: "codex-main",
          started_at: "2026-06-20T06:00:00Z",
        },
      ],
    });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 2,
      jobs: [
        {
          id: 1,
          username: "alice",
          provider_type: "codex",
          account_name: "codex-main",
          started_at: "2026-06-20T06:00:00Z",
        },
      ],
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("2 conversations currently running");
    expect(wrapper.text()).toContain("alice");
    expect(wrapper.text()).toContain("codex/codex-main");
    const updateBtn = wrapper
      .findAll("button")
      .find((b) => b.text().includes("Graceful upgrade to v1.1.0"));
    expect(updateBtn?.attributes("disabled")).toBeUndefined();
    expect(wrapper.text()).not.toContain("Upgrade when conversation ends");
  });

  it("separates queued jobs from running ones in the busy banner", async () => {
    const jobs = [
      {
        id: 1,
        username: "alice",
        provider_type: "codex",
        account_name: "codex-main",
        status: "running" as const,
        started_at: "2026-06-20T06:00:00Z",
      },
      {
        id: 2,
        username: "bob",
        provider_type: "codex",
        account_name: "codex-main",
        status: "queued" as const,
        started_at: "2026-06-20T06:01:00Z",
      },
    ];
    mockUpgradeBusy.mockResolvedValue({ in_flight_jobs: 2, jobs });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 2,
      jobs,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    // Only the running job feeds the headline count; the queued one is broken
    // out separately and tagged.
    expect(wrapper.text()).toContain("1 conversations currently running");
    expect(wrapper.text()).toContain("+1 waiting for a free slot");
    expect(wrapper.text()).toContain("queued");
  });

  it("schedules a graceful upgrade while conversations are running", async () => {
    mockUpgradeBusy.mockResolvedValue({ in_flight_jobs: 1, jobs: [] });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 1,
      jobs: [],
    });
    mockUpgradeApply.mockResolvedValueOnce({
      status: "waiting_for_conversations",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      watchdog_active: false,
    });
    mockConfirm.mockResolvedValue(true);

    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Check"))
      ?.trigger("click");
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Graceful upgrade to v1.1.0"))
      ?.trigger("click");
    await flushPromises();

    expect(mockUpgradeApply).toHaveBeenCalledWith(false);
    expect(wrapper.text()).toContain("Graceful upgrade to v1.1.0 is scheduled");
  });

  it("restores a scheduled graceful upgrade after a page refresh", async () => {
    mockUpgradeStatus.mockResolvedValueOnce({
      phase: "waiting_for_conversations",
      pending_operation: "upgrade",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      started_at: "",
      finished_at: "",
      error: "",
      current_version: "v1.0.0",
      backup_available: false,
    });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 1,
      jobs: [],
      recent_releases: [],
    });
    mockUpgradeApply.mockResolvedValueOnce({
      status: "waiting_for_conversations",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      watchdog_active: false,
    });

    const wrapper = mountAdmin();
    await flushPromises();
    const upgradeButton = wrapper
      .findAll("button")
      .find((button) => button.text().includes("Graceful upgrade to v1.1.0"));
    expect(upgradeButton?.attributes("disabled")).toBeUndefined();
    expect(wrapper.text()).toContain("Graceful upgrade to v1.1.0 is scheduled");

    await upgradeButton?.trigger("click");
    await flushPromises();
    expect(mockUpgradeApply).toHaveBeenCalledWith(false);
    wrapper.unmount();
  });

  it("disables Check while another upgrade is validating", async () => {
    mockUpgradeStatus.mockReset();
    mockUpgradeStatus.mockResolvedValue({
      phase: "validating",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      started_at: "",
      finished_at: "",
      error: "",
      current_version: "v1.0.0",
      backup_available: true,
    });
    const wrapper = mountAdmin();
    await flushPromises();
    const checkBtn = wrapper.findAll("button").find((b) => b.text().includes("Check"));
    expect(checkBtn?.attributes("disabled")).toBeDefined();
  });
});

describe("AdminGlobal database maintenance panel", () => {
  it("hides the panel for non-sqlite backends", async () => {
    mockFetchPublicConfig.mockResolvedValueOnce({ ...baseConfig, backend: "postgres" });
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).not.toContain("Database maintenance");
    expect(wrapper.text()).not.toContain("Optimize database");
    expect(mockDatabaseSize).not.toHaveBeenCalled();
  });

  it("shows the current database size on mount", async () => {
    mockDatabaseSize.mockResolvedValueOnce(7.5 * 1024 * 1024);
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("Current size");
    expect(wrapper.text()).toContain("7.50 MB");
  });

  it("renders 'unknown' when the backend reports zero bytes", async () => {
    mockDatabaseSize.mockResolvedValueOnce(0);
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("unknown");
  });

  it("shows reclaimed bytes and refreshes current size after optimization", async () => {
    mockDatabaseSize.mockResolvedValueOnce(5 * 1024 * 1024);
    mockOptimizeDatabase.mockResolvedValueOnce({
      before_bytes: 5 * 1024 * 1024,
      after_bytes: 1 * 1024 * 1024,
      bytes_reclaimed: 4 * 1024 * 1024,
      expired_sessions_deleted: 3,
      purged_conversations: 2,
      purged_users: 0,
      purged_agents: 0,
    });
    // happy-dom doesn't ship window.confirm — assign a fake so the click
    // handler doesn't bail at the confirmation prompt.
    mockConfirm.mockResolvedValue(true);

    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.text()).toContain("Database maintenance");
    expect(wrapper.text()).toContain("5.00 MB"); // pre-optimize size

    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Optimize database"))
      ?.trigger("click");
    await flushPromises();

    expect(mockOptimizeDatabase).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("Reclaimed");
    expect(wrapper.text()).toContain("4.00 MB");
    expect(wrapper.text()).toContain("3 expired login sessions");
    // The current-size line should now reflect after_bytes (1 MB).
    expect(wrapper.text()).toContain("1.00 MB");
  });

  it("surfaces server errors to the admin", async () => {
    mockOptimizeDatabase.mockRejectedValueOnce(new Error("vacuum failed: disk full"));
    mockConfirm.mockResolvedValue(true);

    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("Optimize database"))
      ?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("vacuum failed: disk full");
  });
});

// Each section below lives in its own child component. These cases assert the
// parent actually mounts them — a child that stops being wired up is dead code
// no unit test of its own would catch.
describe("AdminGlobal section composition", () => {
  const sections = ["database-maintenance", "help-document"];

  for (const testid of sections) {
    it(`renders the ${testid} section through the page`, async () => {
      const wrapper = mountAdmin();
      await flushPromises();
      expect(wrapper.find(`[data-testid="${testid}"]`).exists()).toBe(true);
    });
  }

  it("renders no section and issues no section fetch when the config fails to load", async () => {
    mockFetchPublicConfig.mockRejectedValueOnce(new Error("forbidden"));
    const wrapper = mountAdmin();
    await flushPromises();

    expect(wrapper.text()).toContain("forbidden");
    for (const testid of sections) {
      expect(wrapper.find(`[data-testid="${testid}"]`).exists()).toBe(false);
    }
    expect(mockFetchHelpDoc).not.toHaveBeenCalled();
    expect(mockDatabaseSize).not.toHaveBeenCalled();
  });
});
