import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

import AdminGlobalDatabase from "./AdminGlobalDatabase.vue";

const mockDatabaseSize = vi.fn();
const mockOptimizeDatabase = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminDatabaseSize: (...args: unknown[]) => mockDatabaseSize(...args),
  adminOptimizeDatabase: (...args: unknown[]) => mockOptimizeDatabase(...args),
}));

const mockConfirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockDatabaseSize.mockResolvedValue(2 * 1024 * 1024);
  mockConfirm.mockResolvedValue(true);
});

async function mountDatabase() {
  const wrapper = mount(AdminGlobalDatabase);
  await flushPromises();
  return wrapper;
}

function optimizeButton(wrapper: Awaited<ReturnType<typeof mountDatabase>>) {
  return wrapper.findAll("button").find((b) => b.text().includes("Optimize database"));
}

describe("AdminGlobalDatabase", () => {
  it("shows the current size fetched on mount", async () => {
    mockDatabaseSize.mockResolvedValueOnce(7.5 * 1024 * 1024);
    const wrapper = await mountDatabase();
    expect(wrapper.text()).toContain("7.50 MB");
  });

  it("renders 'unknown' when the backend reports zero bytes", async () => {
    mockDatabaseSize.mockResolvedValueOnce(0);
    const wrapper = await mountDatabase();
    expect(wrapper.text()).toContain("unknown");
  });

  it("still renders the panel when the size fetch fails", async () => {
    mockDatabaseSize.mockRejectedValueOnce(new Error("stat failed"));
    const wrapper = await mountDatabase();
    expect(optimizeButton(wrapper)?.exists()).toBe(true);
    expect(wrapper.text()).not.toContain("stat failed");
  });

  it("skips the VACUUM when the admin declines the confirmation", async () => {
    mockConfirm.mockResolvedValueOnce(false);
    const wrapper = await mountDatabase();

    await optimizeButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(mockOptimizeDatabase).not.toHaveBeenCalled();
  });

  it("reports reclaimed bytes and adopts the post-VACUUM size", async () => {
    mockDatabaseSize.mockResolvedValueOnce(5 * 1024 * 1024);
    mockOptimizeDatabase.mockResolvedValueOnce({
      before_bytes: 5 * 1024 * 1024,
      after_bytes: 1 * 1024 * 1024,
      bytes_reclaimed: 4 * 1024 * 1024,
      expired_sessions_deleted: 3,
      purged_conversations: 2,
      purged_users: 1,
      purged_agents: 2,
    });
    const wrapper = await mountDatabase();

    await optimizeButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("4.00 MB");
    expect(wrapper.text()).toContain("3 expired login sessions");
    expect(wrapper.text()).toContain("2 aged deleted conversations");
    expect(wrapper.text()).toContain("1 aged deleted users");
    expect(wrapper.text()).toContain("2 aged or deleted-user-owned agents");
    // No second /db/size round-trip — the size line reads after_bytes.
    expect(mockDatabaseSize).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("1.00 MB");
  });

  it("omits the purged-sessions clause when nothing expired", async () => {
    mockOptimizeDatabase.mockResolvedValueOnce({
      before_bytes: 2048,
      after_bytes: 1024,
      bytes_reclaimed: 1024,
      expired_sessions_deleted: 0,
      purged_conversations: 0,
      purged_users: 0,
      purged_agents: 0,
    });
    const wrapper = await mountDatabase();

    await optimizeButton(wrapper)?.trigger("click");
    await flushPromises();

    // "expired login sessions" also appears in the section intro, so key the
    // assertion on the result line's own wording.
    expect(wrapper.text()).not.toContain("Purged");
  });

  it("surfaces a failed VACUUM to the admin", async () => {
    mockOptimizeDatabase.mockRejectedValueOnce(new Error("vacuum failed: disk full"));
    const wrapper = await mountDatabase();

    await optimizeButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("vacuum failed: disk full");
  });
});
