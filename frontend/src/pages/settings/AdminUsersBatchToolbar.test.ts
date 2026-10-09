import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import AdminUsersBatchToolbar from "./AdminUsersBatchToolbar.vue";

function mountToolbar(props: Partial<InstanceType<typeof AdminUsersBatchToolbar>["$props"]> = {}) {
  return mount(AdminUsersBatchToolbar, {
    props: {
      selectedCount: 2,
      modelGroups: [{ provider: "claude", models: ["claude-haiku-4-5"] }],
      providerTypes: ["claude", "codex"],
      providerOptionsByType: { claude: ["default", "alt"], codex: ["default-codex"] },
      typeLabels: { claude: "Claude account", codex: "Codex account" },
      busy: false,
      ...props,
    },
  });
}

async function pick(wrapper: ReturnType<typeof mountToolbar>, testid: string, value: string) {
  const sel = wrapper.find(`[data-testid="${testid}"]`);
  (sel.element as HTMLSelectElement).value = value;
  await sel.trigger("change");
}

describe("AdminUsersBatchToolbar", () => {
  it("reports how many rows the actions will hit", () => {
    expect(mountToolbar({ selectedCount: 3 }).text()).toContain("3");
  });

  it("emits the chosen model as a model apply", async () => {
    const wrapper = mountToolbar();
    await pick(wrapper, "batch-model-select", "claude-haiku-4-5");
    await wrapper.find('[data-testid="batch-model-apply"]').trigger("click");

    expect(wrapper.emitted("apply")?.[0][0]).toEqual({
      kind: "model",
      model: "claude-haiku-4-5",
    });
  });

  it("emits the chosen sandbox mode", async () => {
    const wrapper = mountToolbar();
    await pick(wrapper, "batch-sandbox-select", "unrestricted");
    await wrapper.find('[data-testid="batch-sandbox-apply"]').trigger("click");

    expect(wrapper.emitted("apply")?.[0][0]).toEqual({ kind: "sandbox", mode: "unrestricted" });
  });

  it("narrows the account picker to the selected CLI type", async () => {
    const wrapper = mountToolbar();
    await pick(wrapper, "batch-provider-type", "codex");

    const names = wrapper
      .find('[data-testid="batch-provider-select"]')
      .findAll("span")
      .map((o) => o.text());
    expect(names).toContain("default-codex");
    expect(names).not.toContain("alt");
  });

  it("drops a stale account when the CLI type changes", async () => {
    const wrapper = mountToolbar();
    await wrapper.find('[data-testid="batch-provider-account-alt"]').trigger("change");
    await pick(wrapper, "batch-provider-type", "codex");
    await wrapper.find('[data-testid="batch-provider-apply"]').trigger("click");

    expect(wrapper.emitted("apply")?.[0][0]).toEqual({
      kind: "provider",
      providerType: "codex",
      providerNames: [],
    });
  });

  it("emits multiple accounts with the chosen default first", async () => {
    const wrapper = mountToolbar();
    await wrapper.find('[data-testid="batch-provider-account-default"]').trigger("change");
    await wrapper.find('[data-testid="batch-provider-account-alt"]').trigger("change");
    await wrapper.find('[data-testid="batch-default-provider-alt"]').trigger("change");
    await wrapper.find('[data-testid="batch-provider-apply"]').trigger("click");

    expect(wrapper.emitted("apply")?.[0][0]).toEqual({
      kind: "provider",
      providerType: "claude",
      providerNames: ["alt", "default"],
    });
  });

  it("disables every action while a batch request is in flight", () => {
    const wrapper = mountToolbar({ busy: true });
    for (const testid of ["batch-model-apply", "batch-sandbox-apply", "batch-provider-apply"]) {
      expect(wrapper.find(`[data-testid="${testid}"]`).attributes("disabled")).toBeDefined();
    }
  });

  it("captions each action so the shared Apply buttons stay unambiguous", () => {
    const text = mountToolbar().text();
    for (const caption of ["Default model", "Sandbox", "Account"]) {
      expect(text).toContain(caption);
    }
  });

  it("emits clear when the admin drops the selection", async () => {
    const wrapper = mountToolbar();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Clear selection")
      ?.trigger("click");

    expect(wrapper.emitted("clear")).toHaveLength(1);
  });
});
