import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import AdminUsersCreateForm from "./AdminUsersCreateForm.vue";

function mountForm(props: Partial<InstanceType<typeof AdminUsersCreateForm>["$props"]> = {}) {
  return mount(AdminUsersCreateForm, {
    props: {
      providerTypes: ["claude", "codex"],
      providerOptionsByType: { claude: ["default", "alt"], codex: ["default-codex"] },
      typeLabels: { claude: "Claude account", codex: "Codex account" },
      defaultHomeRoot: "/srv/users",
      busy: false,
      ...props,
    },
    global: { stubs: { DirPicker: { template: "<div />" } } },
  });
}

function createButton(wrapper: ReturnType<typeof mountForm>) {
  return wrapper.findAll("button").find((b) => b.text() === "Create");
}

async function fillCredentials(wrapper: ReturnType<typeof mountForm>) {
  const inputs = wrapper.findAll("input");
  await inputs[0].setValue("alice");
  await inputs[1].setValue("hunter2");
}

async function pickBinding(wrapper: ReturnType<typeof mountForm>, ptype: string, name: string) {
  const sel = wrapper.find(`[data-testid="create-binding-${ptype}"]`);
  (sel.element as HTMLSelectElement).value = name;
  await sel.trigger("change");
}

describe("AdminUsersCreateForm", () => {
  it("renders one binding picker per configured CLI type", () => {
    const wrapper = mountForm();
    expect(wrapper.find('[data-testid="create-binding-claude"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="create-binding-codex"]').exists()).toBe(true);
  });

  it("leaves every binding unset so a new user gets no implicit default", () => {
    const wrapper = mountForm();
    const sel = wrapper.find('[data-testid="create-binding-claude"]');
    expect((sel.element as HTMLSelectElement).value).toBe("");
    expect(sel.findAll("option")[0].text()).toBe("none");
  });

  it("keeps Create disabled until credentials and at least one account are set", async () => {
    const wrapper = mountForm();
    expect(createButton(wrapper)?.attributes("disabled")).toBeDefined();

    await fillCredentials(wrapper);
    expect(createButton(wrapper)?.attributes("disabled")).toBeDefined();
    expect(wrapper.find('[data-testid="create-binding-required"]').exists()).toBe(true);

    await pickBinding(wrapper, "codex", "default-codex");
    expect(createButton(wrapper)?.attributes("disabled")).toBeUndefined();
    expect(wrapper.find('[data-testid="create-binding-required"]').exists()).toBe(false);
  });

  it("does not require an account when no provider is configured", async () => {
    const wrapper = mountForm({ providerTypes: [], providerOptionsByType: {} });
    await fillCredentials(wrapper);
    expect(createButton(wrapper)?.attributes("disabled")).toBeUndefined();
  });

  it("falls back to the username when no display name is given", async () => {
    const wrapper = mountForm();
    await fillCredentials(wrapper);
    await pickBinding(wrapper, "claude", "default");
    await createButton(wrapper)?.trigger("click");

    expect(wrapper.emitted("submit")?.[0][0]).toMatchObject({ username: "alice", name: "alice" });
  });

  it("drops empty bindings so an untouched picker doesn't ship a clear instruction", async () => {
    const wrapper = mountForm();
    await fillCredentials(wrapper);
    await pickBinding(wrapper, "claude", "alt");
    await pickBinding(wrapper, "codex", "default-codex");
    await pickBinding(wrapper, "codex", "");
    await createButton(wrapper)?.trigger("click");

    const sent = wrapper.emitted("submit")?.[0][0] as {
      provider_bindings?: Record<string, string>;
    };
    expect(sent.provider_bindings).toEqual({ claude: "alt" });
  });

  it("submits the bindings the admin actually picked", async () => {
    const wrapper = mountForm();
    await fillCredentials(wrapper);
    const sel = wrapper.find('[data-testid="create-binding-claude"]');
    (sel.element as HTMLSelectElement).value = "alt";
    await sel.trigger("change");
    await createButton(wrapper)?.trigger("click");

    expect(wrapper.emitted("submit")?.[0][0]).toMatchObject({
      provider_bindings: { claude: "alt" },
    });
  });

  it("emits cancel without submitting", async () => {
    const wrapper = mountForm();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Cancel")
      ?.trigger("click");

    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("submit")).toBeUndefined();
  });

  it("clears the draft when the parent resets it after a successful create", async () => {
    const wrapper = mountForm();
    await fillCredentials(wrapper);
    wrapper.vm.reset();
    await wrapper.vm.$nextTick();

    expect((wrapper.findAll("input")[0].element as HTMLInputElement).value).toBe("");
  });
});
