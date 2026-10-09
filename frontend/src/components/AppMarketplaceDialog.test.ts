import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import { i18n } from "@/i18n";
import AppMarketplaceDialog from "./AppMarketplaceDialog.vue";

const mockList = vi.fn();
const mockCreate = vi.fn();
const mockUpdate = vi.fn();
const mockDelete = vi.fn();
const mockConfirm = vi.fn();

vi.mock("@/composables/apiMarketplace", () => ({
  listMarketplaceApps: (...args: unknown[]) => mockList(...args),
  createMarketplaceApp: (...args: unknown[]) => mockCreate(...args),
  updateMarketplaceApp: (...args: unknown[]) => mockUpdate(...args),
  deleteMarketplaceApp: (...args: unknown[]) => mockDelete(...args),
}));

vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: (...args: unknown[]) => mockConfirm(...args) }),
}));

const apps = [
  {
    id: "docs",
    name: "Docs",
    description: "Team documentation",
    url: "https://example.com/docs",
    icon_url: "",
    deploy_dir: "/srv/daymug/docs",
    created_by: "alice",
    created_by_name: "Alice",
    created_at: "2026-09-09T00:00:00Z",
  },
  {
    id: "metrics",
    name: "Metrics",
    description: "Product dashboards",
    url: "https://example.com/metrics",
    icon_url: "",
    deploy_dir: "",
    created_by: "bob",
    created_by_name: "Bob",
    created_at: "2026-09-08T00:00:00Z",
  },
];

function el(selector: string) {
  const node = document.body.querySelector(selector);
  if (!node) throw new Error(`not found: ${selector}`);
  return new DOMWrapper(node as Element);
}

function all(selector: string) {
  return [...document.body.querySelectorAll(selector)].map((node) => new DOMWrapper(node));
}

let active: ReturnType<typeof mount> | null = null;

function mountDialog() {
  active = mount(AppMarketplaceDialog, {
    props: { open: true },
    global: { plugins: [i18n] },
    attachTo: document.body,
  });
  return active;
}

beforeEach(() => {
  localStorage.clear();
  mockList.mockReset().mockResolvedValue([...apps]);
  mockCreate.mockReset();
  mockUpdate.mockReset();
  mockDelete.mockReset().mockResolvedValue(undefined);
  mockConfirm.mockReset().mockResolvedValue(true);
  vi.spyOn(window, "open").mockImplementation(() => null);
});

afterEach(() => {
  active?.unmount();
  active = null;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("AppMarketplaceDialog", () => {
  it("sorts by local visit frequency and records each launch", async () => {
    localStorage.setItem(
      "daymug.marketplace.visits",
      JSON.stringify({ metrics: { count: 3, lastVisited: 1 } }),
    );
    mountDialog();
    await flushPromises();

    const cards = all('[data-testid="marketplace-app"]');
    expect(cards.map((card) => card.text())).toEqual([
      expect.stringContaining("Metrics"),
      expect.stringContaining("Docs"),
    ]);

    await cards[1].get("button").trigger("click");
    expect(window.open).toHaveBeenCalledWith(
      "https://example.com/docs",
      "_blank",
      "noopener,noreferrer",
    );
    const stored = JSON.parse(localStorage.getItem("daymug.marketplace.visits") ?? "{}") as {
      docs: { count: number };
    };
    expect(stored.docs.count).toBe(1);
  });

  it("searches app names and descriptions", async () => {
    mountDialog();
    await flushPromises();
    await el('[data-testid="marketplace-search"]').setValue("dashboard");

    const cards = all('[data-testid="marketplace-app"]');
    expect(cards).toHaveLength(1);
    expect(cards[0].text()).toContain("Metrics");
  });

  it("shows the publisher and optional deployment directory", async () => {
    mountDialog();
    await flushPromises();

    const cards = all('[data-testid="marketplace-app"]');
    expect(cards[0].text()).toContain("Published by Alice");
    expect(cards[0].text()).toContain("Deployment directory: /srv/daymug/docs");
    expect(cards[1].text()).toContain("Published by Bob");
    expect(cards[1].text()).not.toContain("Deployment directory:");
  });

  it("submits a new app from the plus form", async () => {
    const created = { ...apps[0], id: "new-app", name: "Calendar" };
    mockCreate.mockResolvedValue(created);
    mountDialog();
    await flushPromises();

    await el('[data-testid="marketplace-add"]').trigger("click");
    await el("#marketplace-name").setValue("Calendar");
    await el("#marketplace-url").setValue("https://example.com/calendar");
    await el("#marketplace-description").setValue("Shared calendar");
    await el("#marketplace-icon-url").setValue("https://example.com/calendar.png");
    await el("#marketplace-deploy-dir").setValue("/srv/daymug/calendar");
    await el('[data-testid="marketplace-app-form"]').trigger("submit");
    await flushPromises();

    expect(mockCreate).toHaveBeenCalledWith({
      name: "Calendar",
      description: "Shared calendar",
      url: "https://example.com/calendar",
      icon_url: "https://example.com/calendar.png",
      deploy_dir: "/srv/daymug/calendar",
    });
    expect(document.body.textContent).toContain("Calendar");
  });

  it("hides the app list while the create form is open", async () => {
    mountDialog();
    await flushPromises();
    expect(all('[data-testid="marketplace-app"]').length).toBeGreaterThan(0);

    await el('[data-testid="marketplace-add"]').trigger("click");
    expect(document.body.querySelector('[data-testid="marketplace-search"]')).toBeNull();
    expect(all('[data-testid="marketplace-app"]')).toHaveLength(0);

    await el('[data-testid="marketplace-app-form"] button[type="button"]').trigger("click");
    expect(all('[data-testid="marketplace-app"]').length).toBeGreaterThan(0);
  });

  it("edits an existing app from settings mode", async () => {
    mockUpdate.mockResolvedValue({ ...apps[0], name: "Docs v2", description: "Revised docs" });
    mountDialog();
    await flushPromises();

    await el('[data-testid="marketplace-settings"]').trigger("click");
    await all('[data-testid="marketplace-edit"]')[0].trigger("click");
    expect((el("#marketplace-name").element as HTMLInputElement).value).toBe("Docs");
    expect((el("#marketplace-deploy-dir").element as HTMLInputElement).value).toBe(
      "/srv/daymug/docs",
    );

    await el("#marketplace-name").setValue("Docs v2");
    await el("#marketplace-description").setValue("Revised docs");
    await el('[data-testid="marketplace-app-form"]').trigger("submit");
    await flushPromises();

    expect(mockUpdate).toHaveBeenCalledWith("docs", {
      name: "Docs v2",
      description: "Revised docs",
      url: "https://example.com/docs",
      icon_url: undefined,
      deploy_dir: "/srv/daymug/docs",
    });
    expect(mockCreate).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("Docs v2");
    expect(document.body.querySelector('[data-testid="marketplace-app-form"]')).toBeNull();
  });

  it("lets any user delete an app from settings mode", async () => {
    mountDialog();
    await flushPromises();

    await el('[data-testid="marketplace-settings"]').trigger("click");
    await all('[data-testid="marketplace-delete"]')[0].trigger("click");
    await flushPromises();

    expect(mockConfirm).toHaveBeenCalled();
    expect(mockDelete).toHaveBeenCalledWith("docs");
    expect(document.body.textContent).not.toContain("Team documentation");
  });
});
