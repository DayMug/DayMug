import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import AdminGlobalUpgrade from "./AdminGlobalUpgrade.vue";

const mockUpgradeCheck = vi.fn();
const mockUpgradeApply = vi.fn();
const mockUpgradeRestart = vi.fn();
const mockUpgradeBusy = vi.fn();
const mockUpgradeStatus = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminUpgradeCheck: (...args: unknown[]) => mockUpgradeCheck(...args),
  adminUpgradeApply: (...args: unknown[]) => mockUpgradeApply(...args),
  adminUpgradeRestart: (...args: unknown[]) => mockUpgradeRestart(...args),
  adminUpgradeBusy: (...args: unknown[]) => mockUpgradeBusy(...args),
  adminUpgradeStatus: (...args: unknown[]) => mockUpgradeStatus(...args),
}));

const mockConfirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));

const idleStatus = {
  phase: "idle",
  old_version: "",
  new_version: "",
  started_at: "",
  finished_at: "",
  error: "",
  current_version: "v1.0.0",
  backup_available: false,
};

function mountPanel(props: Partial<{ currentVersion: string; enabled: boolean }> = {}) {
  return mount(AdminGlobalUpgrade, {
    props: { currentVersion: "v1.0.0", enabled: true, ...props },
  });
}

function clickButton(wrapper: ReturnType<typeof mountPanel>, label: string) {
  return wrapper
    .findAll("button")
    .find((b) => b.text().includes(label))
    ?.trigger("click");
}

beforeEach(() => {
  vi.clearAllMocks();
  mockConfirm.mockResolvedValue(true);
  mockUpgradeBusy.mockResolvedValue({ in_flight_jobs: 0, jobs: [] });
  mockUpgradeStatus.mockResolvedValue(idleStatus);
  mockUpgradeRestart.mockResolvedValue({ status: "restarting", in_flight_jobs: 0, jobs: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("AdminGlobalUpgrade", () => {
  it("renders the current version supplied by the page", async () => {
    const wrapper = mountPanel({ currentVersion: "v2.3.4" });
    await flushPromises();
    expect(wrapper.text()).toContain("v2.3.4");
    wrapper.unmount();
  });

  it("refreshes the running version from an update check", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v2.3.5",
      latest_version: "v2.3.5",
      released_at: "",
      has_update: false,
      download_url: "",
      in_flight_jobs: 0,
    });
    const wrapper = mountPanel({ currentVersion: "v2.3.4" });
    await flushPromises();
    await clickButton(wrapper, "Check");
    await flushPromises();
    expect(wrapper.get('[data-testid="running-version"]').text()).toBe("v2.3.5");
    wrapper.unmount();
  });

  it("falls back to the dev-version label when no version is known", async () => {
    const wrapper = mountPanel({ currentVersion: "" });
    await flushPromises();
    expect(wrapper.text()).toContain("Currently running: dev");
    wrapper.unmount();
  });

  it("skips every upgrade request when the feature is disabled", async () => {
    const wrapper = mountPanel({ enabled: false });
    await flushPromises();
    expect(wrapper.text()).toContain("Self-upgrade is not configured");
    expect(wrapper.text()).not.toContain("Check for updates");
    expect(mockUpgradeStatus).not.toHaveBeenCalled();
    expect(mockUpgradeBusy).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("renders the Update button after a check finds a new version", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "2026-04-30T00:00:00Z",
      has_update: true,
      download_url: "https://example.com/bin",
      in_flight_jobs: 0,
    });
    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Check");
    await flushPromises();
    expect(wrapper.text()).toContain("Graceful upgrade to v1.1.0");
    wrapper.unmount();
  });

  it("reports already-up-to-date when the server has no newer build", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.0.0",
      released_at: "",
      has_update: false,
      download_url: "",
      in_flight_jobs: 0,
    });
    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Check");
    await flushPromises();
    expect(wrapper.text()).toContain("Already on the latest version");
    wrapper.unmount();
  });

  it("explains a newer release that skipped this platform", async () => {
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "2026-04-30T00:00:00Z",
      has_update: false,
      platform_supported: false,
      download_url: "",
      in_flight_jobs: 0,
    });
    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Check");
    await flushPromises();
    expect(wrapper.text()).toContain("no binary for this machine's OS/architecture");
    expect(wrapper.text()).not.toContain("Already on the latest version");
    wrapper.unmount();
  });

  it("separates queued jobs from running ones in the busy banner", async () => {
    mockUpgradeBusy.mockResolvedValue({
      in_flight_jobs: 2,
      jobs: [
        {
          id: 1,
          username: "alice",
          provider_type: "codex",
          account_name: "codex-main",
          status: "running",
          started_at: "2026-06-20T06:00:00Z",
        },
        {
          id: 2,
          username: "bob",
          provider_type: "codex",
          account_name: "codex-main",
          status: "queued",
          started_at: "2026-06-20T06:01:00Z",
        },
      ],
    });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.text()).toContain("1 conversations currently running");
    expect(wrapper.text()).toContain("+1 waiting for a free slot");
    expect(wrapper.text()).toContain("codex/codex-main");
    wrapper.unmount();
  });

  it("lists waiting and background work apart from running conversations", async () => {
    mockUpgradeBusy.mockResolvedValue({
      in_flight_jobs: 1,
      jobs: [
        {
          id: 1,
          username: "wendy",
          provider_type: "claude",
          account_name: "hailing",
          status: "waiting",
          started_at: "2026-09-27T05:43:04Z",
        },
        {
          id: 0,
          username: "ada",
          provider_type: "claude",
          account_name: "hailing",
          status: "background",
          started_at: "2026-09-27T05:47:08Z",
        },
      ],
    });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.text()).toContain("0 conversations currently running");
    expect(wrapper.text()).toContain("+1 background task(s) still running");
    expect(wrapper.text()).toContain("waiting for reply");
    expect(wrapper.text()).toContain("ada");
    wrapper.unmount();
  });

  it("requests a graceful restart after the admin confirms", async () => {
    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Graceful restart");
    await flushPromises();
    expect(mockUpgradeRestart).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain("Restart requested");
    wrapper.unmount();
  });

  it("does not restart when the admin cancels the confirmation", async () => {
    mockConfirm.mockResolvedValue(false);
    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Graceful restart");
    await flushPromises();
    expect(mockUpgradeRestart).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("disables Check while another upgrade is validating", async () => {
    mockUpgradeStatus.mockResolvedValue({ ...idleStatus, phase: "validating" });
    const wrapper = mountPanel();
    await flushPromises();
    const checkBtn = wrapper.findAll("button").find((b) => b.text().includes("Check"));
    expect(checkBtn?.attributes("disabled")).toBeDefined();
    wrapper.unmount();
  });

  it("restores a graceful upgrade scheduled before the page was reloaded", async () => {
    mockUpgradeStatus.mockResolvedValueOnce({
      ...idleStatus,
      phase: "waiting_for_conversations",
      pending_operation: "upgrade",
      new_version: "v1.1.0",
    });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 1,
      jobs: [],
    });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.text()).toContain("Graceful upgrade to v1.1.0 is scheduled");
    wrapper.unmount();
  });

  it("emits upgrade-succeeded once the watchdog reports a healthy new build", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    mockUpgradeCheck.mockResolvedValueOnce({
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      released_at: "",
      has_update: true,
      download_url: "",
      in_flight_jobs: 0,
      jobs: [],
    });
    mockUpgradeApply.mockResolvedValueOnce({
      status: "upgrading",
      old_version: "v1.0.0",
      new_version: "v1.1.0",
      watchdog_active: true,
    });

    const wrapper = mountPanel();
    await flushPromises();
    await clickButton(wrapper, "Check");
    await flushPromises();
    await clickButton(wrapper, "Graceful upgrade to v1.1.0");
    await flushPromises();
    expect(wrapper.emitted("upgrade-succeeded")).toBeUndefined();

    mockUpgradeStatus.mockResolvedValue({ ...idleStatus, phase: "ok", new_version: "v1.1.0" });
    await vi.advanceTimersByTimeAsync(3000);
    await flushPromises();

    expect(wrapper.emitted("upgrade-succeeded")).toHaveLength(1);
    // The stale check result is cleared so the "Update to vX" button goes away.
    expect(wrapper.text()).not.toContain("Graceful upgrade to v1.1.0");
    wrapper.unmount();
  });
});
