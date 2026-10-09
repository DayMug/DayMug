import { describe, it, expect, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { ref } from "vue";
import UserForm from "./UserForm.vue";

vi.mock("@/composables/useApi", () => ({
  browseDirs: vi.fn().mockResolvedValue({ current: "/home/admin", parent: "", dirs: [] }),
  mkdirBrowseDir: vi.fn(),
}));

// UserForm now defaults work_dir to the signed-in human's own work_dir
// (the same root the picker is jailed to). Both admins and non-admins
// follow the rule, so the form just needs authMe.work_dir to be set.
vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({
    authMe: ref({
      id: "u0",
      username: "admin",
      name: "Admin",
      is_admin: true,
      bark_url: "",
      pushdeer_key: "",
      notification_channel: "",
      work_dir: "/home/admin",
    }),
  }),
}));

vi.mock("@/composables/useUsers", () => ({
  useUsers: () => ({ currentUser: ref({ id: "u0", work_dir: "/home/admin" }) }),
}));

vi.mock("@/composables/useModelRegistry", () => ({
  loadModelRegistry: vi.fn().mockResolvedValue({
    providers: [
      {
        name: "claude",
        models: ["claude-opus-4-8"],
        latest: "claude-opus-4-8",
        capabilities: {},
      },
      {
        name: "codex",
        models: ["gpt-5.6-sol"],
        latest: "gpt-5.6-sol",
        capabilities: {},
      },
    ],
    default_provider: "claude",
  }),
}));

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

describe("UserForm", () => {
  it("defaults work_dir to the human owner's home in add mode", async () => {
    // The form pulls the default straight from authMe.work_dir — no
    // extra round-trip to /api/browse-dirs — so the new agent always
    // starts inside the same jail the picker enforces.
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    await flushPromises();
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    expect(dirPicker.exists()).toBe(true);
    expect(dirPicker.props("modelValue")).toBe("/home/admin");
    // The picker is told its jail root so it can disable Up at the home
    // boundary and re-anchor a stale modelValue.
    expect(dirPicker.props("rootPath")).toBe("/home/admin");
  });

  it("renders basic info fields", () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    expect(wrapper.find("input[placeholder='Username']").exists()).toBe(true);
    expect(
      wrapper.find("input[placeholder='Image URL or initials, e.g. https://... / BG']").exists(),
    ).toBe(true);
  });

  it("starts with empty persona fields and no preset templates in add mode", () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    const role = wrapper.find('textarea[placeholder="Describe this agent\'s role..."]');

    expect((role.element as HTMLTextAreaElement).value).toBe("");
    // The persona is the only prompt field; the free-text Skills box is gone.
    expect(wrapper.findAll("textarea")).toHaveLength(1);
    expect(wrapper.text()).not.toContain("Persona template");
    expect(wrapper.text()).not.toContain("Code Reviewer");
  });

  it("disables submit when name or work_dir is empty", () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    const submitBtn = wrapper.findAll("button").find((b) => b.text() === "Add");
    expect((submitBtn!.element as HTMLButtonElement).disabled).toBe(true);
  });

  it("emits submit with form data", async () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });

    await wrapper.find("input[placeholder='Username']").setValue("Alice");
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/proj");
    await wrapper.vm.$nextTick();

    await wrapper
      .find("input[placeholder='Image URL or initials, e.g. https://... / BG']")
      .setValue("https://example.com/avatar.png");
    const submitBtn = wrapper.findAll("button").find((b) => b.text() === "Add");
    await submitBtn!.trigger("click");

    expect(wrapper.emitted("submit")).toBeTruthy();
    const data = wrapper.emitted("submit")![0][0] as Record<string, unknown>;
    expect(data.name).toBe("Alice");
    expect(data.work_dir).toBe("/home/admin/proj");
    expect(data.avatar).toBe("https://example.com/avatar.png");
  });

  it("emits cancel on cancel button click", async () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    const cancelBtn = wrapper.findAll("button").find((b) => b.text() === "Cancel");
    await cancelBtn!.trigger("click");
    expect(wrapper.emitted("cancel")).toBeTruthy();
  });

  it("offers an optional default model without an Agent-level thinking control", async () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    await flushPromises();

    const select = wrapper.find('[data-testid="default-model"]');
    expect(select.findAll("option").map((option) => option.text())).toContain("claude-opus-4-8");
    await select.setValue("claude-opus-4-8");
    expect(wrapper.find('[data-testid="think-level"]').exists()).toBe(false);
    await wrapper.find("input[placeholder='Username']").setValue("Agent");
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/agent");
    await wrapper.vm.$nextTick();
    await wrapper
      .findAll("button")
      .find((button) => button.text() === "Add")!
      .trigger("click");

    expect(wrapper.emitted("submit")![0][0]).toMatchObject({
      default_model: "claude-opus-4-8",
      think_level: "",
    });
  });

  it("preserves a hidden API-configured think level during unrelated edits", async () => {
    const wrapper = mount(UserForm, {
      props: {
        mode: "edit",
        initialData: {
          name: "Agent",
          work_dir: "/home/admin/agent",
          avatar: "",
          role_definition: "",
          default_model: "claude-opus-4-8",
          think_level: "high",
          mcp_config: "",
          claude_md_content: "",
          manage_claude_md: false,
        },
      },
    });
    expect(wrapper.find('[data-testid="think-level"]').exists()).toBe(false);
    await wrapper
      .findAll("button")
      .find((button) => button.text() === "Save")!
      .trigger("click");
    expect(wrapper.emitted("submit")![0][0]).toMatchObject({ think_level: "high" });
  });

  it("defaults case-file mode off and submits it when ticked", async () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    await flushPromises();

    const toggle = wrapper.find('[data-testid="case-mode"]');
    expect((toggle.element as HTMLInputElement).checked).toBe(false);

    await wrapper.find("input[placeholder='Username']").setValue("Agent");
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/agent");
    await wrapper.vm.$nextTick();
    await toggle.setValue(true);
    await wrapper
      .findAll("button")
      .find((button) => button.text() === "Add")!
      .trigger("click");

    expect(wrapper.emitted("submit")![0][0]).toMatchObject({ case_mode: true });
  });

  it("insets the custom model chevron from the select edge", () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    const select = wrapper.find('[data-testid="default-model"]');
    const chevron = wrapper.find('[data-testid="default-model-chevron"]');

    expect(select.classes()).toEqual(expect.arrayContaining(["appearance-none", "pr-12"]));
    expect(chevron.classes()).toEqual(expect.arrayContaining(["right-4", "pointer-events-none"]));
  });

  it("loads initialData into form fields", () => {
    const wrapper = mount(UserForm, {
      props: {
        mode: "edit",
        initialData: {
          name: "Bob",
          work_dir: "/home/bob",
          avatar: "B",
          role_definition: "Dev",
          mcp_config: "",
          claude_md_content: "",
          manage_claude_md: false,
        },
      },
    });

    const nameInput = wrapper.find("input[placeholder='Username']").element as HTMLInputElement;
    expect(nameInput.value).toBe("Bob");
    expect(wrapper.find('[data-testid="bot-integration"]').exists()).toBe(false);
  });

  it("submits only Agent configuration fields", async () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    await wrapper.find("input[placeholder='Username']").setValue("Slack Agent");
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/slack");
    await wrapper.vm.$nextTick();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Add")!
      .trigger("click");

    const data = wrapper.emitted("submit")![0][0] as Record<string, unknown>;
    expect(data.kind).toBeUndefined();
    expect(data.permission_rules).toBeUndefined();
    expect(data.name).toBe("Slack Agent");
    expect(wrapper.text()).not.toContain("Permission Rules");
  });

  it("hides platform credentials for Agent subjects", () => {
    const wrapper = mount(UserForm, { props: { mode: "add" } });
    expect(wrapper.find('[data-testid="bot-integration"]').exists()).toBe(false);
  });

  it("carries the advanced fields through submit instead of blanking them", async () => {
    // Regression guard: these three used to live on the edit page as separate
    // refs and were submitted as empty strings whenever their seeding watcher
    // ran before the agent roster arrived, silently deleting server-side MCP
    // config and CLAUDE.md.
    const wrapper = mount(UserForm, {
      props: {
        mode: "edit",
        initialData: {
          name: "Bob",
          work_dir: "/home/bob",
          avatar: "B",
          role_definition: "Dev",
          mcp_config: '{"mcpServers":{}}',
          claude_md_content: "# Bob rules",
          manage_claude_md: true,
        },
      },
    });

    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click");

    expect(wrapper.emitted("submit")![0][0]).toMatchObject({
      mcp_config: '{"mcpServers":{}}',
      claude_md_content: "# Bob rules",
      manage_claude_md: true,
    });
  });

  it("does not render the retired advanced settings in edit mode", () => {
    const wrapper = mount(UserForm, {
      props: {
        mode: "edit",
        initialData: {
          name: "Bob",
          work_dir: "/home/bob",
          avatar: "B",
          role_definition: "Dev",
          mcp_config: "",
          claude_md_content: "stale",
          manage_claude_md: false,
        },
      },
    });

    expect(wrapper.find("textarea.mcp-editor").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("CLAUDE.md");
  });

  it("shows Add button in add mode and Save button in edit mode", () => {
    const addWrapper = mount(UserForm, { props: { mode: "add" } });
    expect(addWrapper.findAll("button").some((b) => b.text() === "Add")).toBe(true);

    const editWrapper = mount(UserForm, { props: { mode: "edit" } });
    expect(editWrapper.findAll("button").some((b) => b.text() === "Save")).toBe(true);
  });
});
