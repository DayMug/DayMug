import { describe, it, expect, beforeEach } from "vitest";
import { ref, effectScope, nextTick, defineComponent, h } from "vue";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { useDocumentTitle, _internals } from "./useDocumentTitle";
import type { Conversation } from "./useApi";

function makeConv(id: string, title: string): Conversation {
  return {
    id,
    user_id: "u1",
    title,
    work_dir: "",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    provider: "",
    model: "",
    created_at: "",
    updated_at: "",
  };
}

describe("buildTitle", () => {
  const { buildTitle, DEFAULT_TITLE } = _internals;

  it("returns the default when title is empty", () => {
    expect(buildTitle("")).toBe(DEFAULT_TITLE);
    expect(buildTitle("   ")).toBe(DEFAULT_TITLE);
    expect(buildTitle(undefined)).toBe(DEFAULT_TITLE);
  });

  it("returns the trimmed title without a brand suffix", () => {
    expect(buildTitle("Project X")).toBe("Project X");
    expect(buildTitle("  trimmed  ")).toBe("trimmed");
  });
});

describe("useDocumentTitle", () => {
  beforeEach(() => {
    document.title = "DayMug";
  });

  it("sets the tab title to the active conversation's title", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "First"), makeConv("c2", "Second")]);
    const currentId = ref("c2");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("Second");

    scope.stop();
  });

  it("falls back to the default when no conversation is selected", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "First")]);
    const currentId = ref("");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("DayMug");

    scope.stop();
  });

  it("falls back to the default when the active conversation has an empty title", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "")]);
    const currentId = ref("c1");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("DayMug");

    scope.stop();
  });

  it("reacts when the active conversation's title is updated", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "")]);
    const currentId = ref("c1");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("DayMug");

    // Simulate backend auto-title arriving via refresh
    conversations.value = [makeConv("c1", "Generated Title")];
    await nextTick();
    expect(document.title).toBe("Generated Title");

    scope.stop();
  });

  it("reacts when switching to a different conversation", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "Alpha"), makeConv("c2", "Beta")]);
    const currentId = ref("c1");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("Alpha");

    currentId.value = "c2";
    await nextTick();
    expect(document.title).toBe("Beta");

    scope.stop();
  });

  it("leaves the title alone on bare-layout routes so the page owns it", async () => {
    // PreviewPage (a lazily loaded, bare-layout route) sets the tab title to
    // the opened file's name. The chat-title mirror must not clobber it when
    // its watcher re-fires — otherwise a freshly opened tab flips back to the
    // brand. Simulate the page having set the filename, then poke the mirror's
    // deps and assert it stays put.
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/chat", name: "chat", component: { template: "<div/>" } },
        {
          path: "/file",
          name: "file",
          component: { template: "<div/>" },
          meta: { layout: "bare" },
        },
      ],
    });
    await router.push("/file");
    await router.isReady();

    const conversations = ref<Conversation[]>([makeConv("c1", "Alpha")]);
    const currentId = ref("c1");
    const Comp = defineComponent({
      setup() {
        useDocumentTitle(conversations, currentId);
        return () => h("div");
      },
    });
    mount(Comp, { global: { plugins: [router] } });

    document.title = "sheet.xlsx";
    conversations.value = [makeConv("c1", "Renamed")];
    currentId.value = "c1";
    await nextTick();
    expect(document.title).toBe("sheet.xlsx");
  });

  it("restores the default title when the scope is disposed", async () => {
    const conversations = ref<Conversation[]>([makeConv("c1", "Alpha")]);
    const currentId = ref("c1");

    const scope = effectScope();
    scope.run(() => useDocumentTitle(conversations, currentId));

    await nextTick();
    expect(document.title).toBe("Alpha");

    scope.stop();
    expect(document.title).toBe("DayMug");
  });
});
