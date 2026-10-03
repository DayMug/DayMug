import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import AdminUsersEditRow from "./AdminUsersEditRow.vue";
import type { User } from "@/composables/useApi";

const baseUser = {
  id: "u1",
  name: "Alice",
  username: "alice",
  email: "alice@example.com",
  is_admin: false,
  disabled: false,
  work_dir: "/home/alice",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as unknown as User;

function mountRow(
  user: Partial<User> = {},
  props: Partial<InstanceType<typeof AdminUsersEditRow>["$props"]> = {},
) {
  return mount(AdminUsersEditRow, {
    props: {
      user: { ...baseUser, ...user } as User,
      providerTypes: ["claude", "codex"],
      providerOptionsByType: { claude: ["default", "alt"], codex: ["default-codex"] },
      typeLabels: { claude: "Claude account", codex: "Codex account" },
      modelGroups: [{ provider: "claude", models: ["claude-haiku-4-5"] }],
      isSelf: false,
      busy: false,
      ...props,
    },
    global: { stubs: { DirPicker: { template: "<div />" } } },
  });
}

function saveButton(wrapper: ReturnType<typeof mountRow>) {
  return wrapper.findAll("button").find((b) => b.text() === "Save");
}

describe("AdminUsersEditRow", () => {
  it("checks the accounts already granted per CLI type", () => {
    const wrapper = mountRow({
      provider_accounts: { claude: ["alt"], codex: ["default-codex"] },
    });
    const granted = (id: string) =>
      (wrapper.find(`[data-testid="${id}"]`).element as HTMLInputElement).checked;

    expect(granted("edit-account-claude-alt")).toBe(true);
    expect(granted("edit-account-claude-default")).toBe(false);
    expect(granted("edit-account-codex-default-codex")).toBe(true);
  });

  it("seeds grants from the single-binding map when provider_accounts is absent", () => {
    const wrapper = mountRow({ provider_bindings: { claude: "alt" } });
    const alt = wrapper.find('[data-testid="edit-account-claude-alt"]');
    expect((alt.element as HTMLInputElement).checked).toBe(true);
  });

  it("submits the promoted account first so the backend reads it as the default", async () => {
    const wrapper = mountRow({ provider_accounts: { claude: ["default"] } });

    await wrapper.find('[data-testid="edit-account-claude-alt"]').trigger("change");
    await wrapper.find('[data-testid="edit-default-account-claude-alt"]').trigger("change");
    await saveButton(wrapper)?.trigger("click");

    expect(wrapper.emitted("save")?.[0][0]).toMatchObject({
      provider_accounts: { claude: ["alt", "default"] },
    });
  });

  it("never ships the single-value map alongside provider_accounts", async () => {
    const wrapper = mountRow({ provider_accounts: { claude: ["default"] } });
    await saveButton(wrapper)?.trigger("click");

    const payload = wrapper.emitted("save")?.[0][0] as Record<string, unknown>;
    expect(payload.provider_bindings).toBeUndefined();
  });

  it("only offers the default radio for accounts that are granted", async () => {
    const wrapper = mountRow({ provider_accounts: { claude: [] } });
    expect(wrapper.find('[data-testid="edit-default-account-claude-alt"]').exists()).toBe(false);

    await wrapper.find('[data-testid="edit-account-claude-alt"]').trigger("change");
    expect(wrapper.find('[data-testid="edit-default-account-claude-alt"]').exists()).toBe(true);
  });

  it("stops the signed-in admin from demoting themselves", () => {
    const wrapper = mountRow({}, { isSelf: true });
    const adminBox = wrapper.findAll('input[type="checkbox"]').at(-1);
    expect(adminBox?.attributes("disabled")).toBeDefined();
  });

  it("does not emit save while a previous save is still in flight", async () => {
    const wrapper = mountRow({}, { busy: true });
    await saveButton(wrapper)?.trigger("click");
    expect(wrapper.emitted("save")).toBeUndefined();
  });

  it("emits cancel without emitting a save", async () => {
    const wrapper = mountRow();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Cancel")
      ?.trigger("click");

    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("save")).toBeUndefined();
  });

  it("seeds the model picker from the user's current default", () => {
    const wrapper = mountRow({ default_model: "claude-haiku-4-5" });
    const picker = wrapper.find('[data-testid="edit-default-model"]');
    expect((picker.element as HTMLSelectElement).value).toBe("claude-haiku-4-5");
  });
});
