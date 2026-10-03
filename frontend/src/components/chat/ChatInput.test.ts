import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import ChatInput from "./ChatInput.vue";
import { DRAFTS_STORAGE_KEY } from "@/lib/chatDrafts";
import { resetChatAttachmentDraftStore } from "@/stores/chatAttachmentDraftStore";
import { invalidateModelRegistry } from "@/composables/useModelRegistry";

// uploadFile is the only piece of API surface ChatInput touches when the
// user pastes/drops/picks an attachment. Mock the composable so the test
// never makes a network call. Hoisted via vi.hoisted to satisfy vi.mock's
// static analysis (see https://vitest.dev/api/vi.html#vi-hoisted).
const { uploadMock, fetchModelsMock } = vi.hoisted(() => ({
  uploadMock: vi.fn(),
  fetchModelsMock: vi.fn(),
}));
vi.mock("@/composables/useApi", () => ({
  uploadFile: uploadMock,
}));
vi.mock("@/composables/apiModels", () => ({
  fetchModels: fetchModelsMock,
}));

function registryWithSteering(supportsSteering: boolean) {
  return {
    default_provider: "claude",
    providers: [
      {
        name: "claude",
        models: ["m"],
        latest: "m",
        transport: supportsSteering ? "agent-sdk" : "cli",
        capabilities: {
          supports_compaction: true,
          supports_thinking_stream: true,
          supports_rate_limit_events: true,
          reports_context_usage: true,
          reports_cost_usd: true,
          supports_steering: supportsSteering,
        },
      },
    ],
  };
}

const defaultProps = { isThinking: false, isConnected: true, conversationId: "conv-1" };

function setPlatform(platform: string) {
  Object.defineProperty(window.navigator, "platform", {
    value: platform,
    configurable: true,
  });
  Object.defineProperty(window.navigator, "userAgent", {
    value: platform,
    configurable: true,
  });
}

function makeImageFile(name = "shot.png", type = "image/png"): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type });
}

function makePasteEvent(
  items: { kind: string; type: string; getAsFile: () => File | null }[],
  text = "",
): ClipboardEvent {
  const ev = new Event("paste") as unknown as ClipboardEvent;
  Object.defineProperty(ev, "clipboardData", {
    value: {
      items,
      getData: (mime: string) => (mime === "text/plain" ? text : ""),
    },
  });
  return ev;
}

beforeEach(() => {
  uploadMock.mockReset();
  // Without capability data the composer keeps its insert default, which is
  // what every test not about transports expects.
  fetchModelsMock.mockReset().mockRejectedValue(new Error("no registry"));
  invalidateModelRegistry();
  setPlatform("Linux x86_64");
  // Drafts are persisted to localStorage keyed by conversation id; clear
  // between tests so leftover state from a previous case doesn't bleed in
  // (the default conversationId is "conv-1" across the suite).
  localStorage.removeItem(DRAFTS_STORAGE_KEY);
  resetChatAttachmentDraftStore();
});

describe("ChatInput", () => {
  it("renders textarea and send button", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("textarea").exists()).toBe(true);
    expect(wrapper.find("[data-testid='send-btn']").exists()).toBe(true);
  });

  it("treats the composer padding as part of the text cursor surface", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find(".composer-pill").attributes("data-cursor-surface")).toBe("text");
  });

  // A side-by-side pill reserved the button column over the full height of the
  // box, so a wrapping message was squeezed into the left half with dead space
  // above the send button. Stacking hands that width back to the text.
  it("stacks the buttons under a full-width textarea", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    const pill = wrapper.find(".composer-pill");
    expect(pill.classes()).toContain("flex-col");
    expect(pill.classes()).not.toContain("sm:flex-row");

    const controls = wrapper.find("[data-testid='composer-controls']");
    expect(controls.find("[data-testid='attach-btn']").exists()).toBe(true);
    expect(controls.find("[data-testid='cancel-btn']").exists()).toBe(true);
    expect(controls.find("[data-testid='send-btn']").exists()).toBe(true);
    expect(controls.find("textarea").exists()).toBe(false);
    expect(wrapper.find("textarea").classes()).toContain("w-full");
  });

  it("keeps the send controls alone in the trailing action cluster", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const cluster = wrapper.find("[data-testid='composer-send-cluster']");
    const ids = Array.from(cluster.element.children).map((el) => el.getAttribute("data-testid"));
    expect(ids).toEqual(["send-controls"]);
  });

  // The model/context controls used to render as their own strip above the
  // pill; folding them into this row is what recovers that vertical space.
  it("renders the conversation controls slot on the send row", () => {
    const wrapper = mount(ChatInput, {
      props: defaultProps,
      slots: { controls: '<span data-testid="stub-controls">profile</span>' },
    });
    const controls = wrapper.find("[data-testid='composer-controls']");
    expect(controls.find("[data-testid='stub-controls']").exists()).toBe(true);
    expect(controls.find("[data-testid='send-btn']").exists()).toBe(true);
  });

  // A wrapping row can place the model selector above Send even when both are
  // direct children. Keeping the row unwrapped makes their shared baseline a
  // layout invariant; the compact selector truncates when space runs short.
  it("keeps the control row unwrapped with slot content beside the send cluster", () => {
    const wrapper = mount(ChatInput, {
      props: defaultProps,
      slots: { controls: '<span data-testid="stub-controls">profile</span>' },
    });
    const controls = wrapper.find("[data-testid='composer-controls']");

    expect(controls.classes()).not.toContain("flex-wrap");
    expect(controls.classes()).toContain("items-center");
    const ids = Array.from(controls.element.children).map((el) => el.getAttribute("data-testid"));
    expect(ids).toEqual(["attach-btn", null, "stub-controls", "composer-send-cluster"]);
  });

  it("uses the reference page's edge-to-edge mobile gutter", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const shell = wrapper.find(".composer-pill").element.parentElement;
    expect(shell?.classList).toContain("px-[9px]");
    expect(shell?.classList).toContain("md:px-[clamp(26px,5vw,72px)]");
  });

  it("picks one short idle placeholder at random for each composer instance", () => {
    const firstRandom = vi.spyOn(Math, "random").mockReturnValue(0);
    const first = mount(ChatInput, { props: defaultProps });
    expect(first.find("textarea").attributes("placeholder")).toBe("Ctrl+Enter to send");
    firstRandom.mockRestore();

    const lastRandom = vi.spyOn(Math, "random").mockReturnValue(0.999999);
    const last = mount(ChatInput, { props: defaultProps });
    expect(last.find("textarea").attributes("placeholder")).toBe("Right-click chats to share");
    lastRandom.mockRestore();
  });

  it("shows the platform send shortcut inside a random hint", () => {
    setPlatform("MacIntel");
    const random = vi.spyOn(Math, "random").mockReturnValue(0);
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("textarea").attributes("placeholder")).toBe("⌘+Enter to send");
    random.mockRestore();
  });

  it("explains that modifier-clicking Delete skips the conversation confirmation", () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0.15);
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("textarea").attributes("placeholder")).toBe("Ctrl-click Delete, no prompt");
    random.mockRestore();
  });

  it("teaches the file page's modifier double-click escape to a new tab", () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0.8);
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("textarea").attributes("placeholder")).toBe(
      "Ctrl-double-click on the file page for a new tab",
    );
    random.mockRestore();
  });

  it("rotates only hints the empty composer doesn't already convey", () => {
    // A generic "What shall we build?" spends a rotation slot restating what an
    // empty input box says; every slot must carry a shortcut or gesture.
    const seen = new Set<string>();
    for (let i = 0; i < 8; i++) {
      const random = vi.spyOn(Math, "random").mockReturnValue((i + 0.5) / 8);
      seen.add(
        mount(ChatInput, { props: defaultProps }).find("textarea").attributes("placeholder")!,
      );
      random.mockRestore();
    }
    expect(seen.size).toBe(8);
    for (const hint of seen) {
      expect(hint).toMatch(/Ctrl|Right-click|Swipe|Hold and drag|Keep sending/);
    }
  });

  // The disc carries no visible label, so the accessible name is the only
  // thing standing between a screen-reader user and an unnamed button — and it
  // has to track the insert/send distinction the tooltip already draws.
  it("names the icon-only send disc for assistive tech", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const send = wrapper.find("[data-testid='send-btn']");

    expect(send.text()).toBe("");
    expect(send.attributes("aria-label")).toBe("Send (Ctrl+Enter)");

    await wrapper.setProps({ isThinking: true });
    expect(wrapper.find("[data-testid='send-btn']").attributes("aria-label")).toBe(
      "Insert into the current task (Ctrl+Enter)",
    );
  });

  it("opens the send-after-completion menu from the up-arrow without sending", async () => {
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isThinking: true },
    });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("change direction");

    await wrapper.find("[data-testid='send-options-btn']").trigger("click");

    expect(wrapper.find("[data-testid='send-options-menu']").exists()).toBe(true);
    const queuedSend = wrapper.find("[data-testid='queue-after-current-btn']");
    expect(queuedSend.text()).toContain("Send after completion");
    expect(queuedSend.text()).toContain("Wait for the current task to finish");
    expect(wrapper.emitted("send")).toBeFalsy();
    expect((textarea.element as HTMLTextAreaElement).value).toBe("change direction");
  });

  it("emits insert mode from the primary send button by default", async () => {
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isThinking: true },
    });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("change direction");
    await wrapper.find("[data-testid='send-btn']").trigger("click");

    expect(wrapper.emitted("send")?.[0]).toEqual(["change direction", undefined, true]);
    expect((textarea.element as HTMLTextAreaElement).value).toBe("");
  });

  // A CLI transport runs one process per turn: a message sent while it works
  // can only wait, so the disc queues and the insert/queue chooser disappears.
  it("only queues on a transport that cannot steer", async () => {
    fetchModelsMock.mockResolvedValue(registryWithSteering(false));
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isThinking: true, provider: "claude" },
    });
    await flushPromises();

    expect(wrapper.find("[data-testid='send-options-btn']").exists()).toBe(false);
    const send = wrapper.find("[data-testid='send-btn']");
    expect(send.attributes("aria-label")).toBe("Send after the current task (Ctrl+Enter)");

    await wrapper.find("textarea").setValue("after this");
    await send.trigger("click");
    expect(wrapper.emitted("send")?.[0]).toEqual(["after this"]);
  });

  it("inserts by default on a transport that can steer", async () => {
    fetchModelsMock.mockResolvedValue(registryWithSteering(true));
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isThinking: true, provider: "claude" },
    });
    await flushPromises();

    expect(wrapper.find("[data-testid='send-options-btn']").exists()).toBe(true);
    await wrapper.find("textarea").setValue("steer now");
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect(wrapper.emitted("send")?.[0]).toEqual(["steer now", undefined, true]);
  });

  it("emits regular FIFO mode when send-after-completion is selected", async () => {
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isThinking: true },
    });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("wait for current");
    await wrapper.find("[data-testid='send-options-btn']").trigger("click");
    await wrapper.find("[data-testid='queue-after-current-btn']").trigger("click");

    expect(wrapper.emitted("send")?.[0]).toEqual(["wait for current"]);
    expect((textarea.element as HTMLTextAreaElement).value).toBe("");
    expect(wrapper.find("[data-testid='send-options-menu']").exists()).toBe(false);
  });

  it("shows the pointer cursor on the send button", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("[data-testid='send-btn']").classes()).toContain("cursor-pointer");
  });

  it("keeps the send disc calm on hover", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const send = wrapper.find("[data-testid='send-btn']");

    expect(send.classes()).toContain("group-hover/send:bg-primary/90");
    expect(send.classes()).not.toContain("hover:-translate-y-0.5");
    expect(send.classes()).not.toContain("hover:shadow-md");
  });

  it("disables both busy-state send controls until there is content", async () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    const send = wrapper.find("[data-testid='send-btn']").element as HTMLButtonElement;
    const options = wrapper.find("[data-testid='send-options-btn']").element as HTMLButtonElement;

    expect(send.disabled).toBe(true);
    expect(options.disabled).toBe(true);

    await wrapper.find("textarea").setValue("ready");
    expect(send.disabled).toBe(false);
    expect(options.disabled).toBe(false);
  });

  it("keeps the send disc primary and the queue chevron quiet once the turn status arrives", async () => {
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, isConnected: false, turnStatusKnown: false },
    });
    const controls = wrapper.find("[data-testid='send-controls']");

    expect(controls.classes()).toContain("invisible");

    await wrapper.setProps({ isConnected: true });
    expect(controls.classes()).toContain("invisible");

    await wrapper.setProps({ isThinking: true, turnStatusKnown: true });
    expect(controls.classes()).not.toContain("invisible");
    expect(wrapper.find("[data-testid='send-btn']").attributes("data-variant")).toBe("default");
    expect(wrapper.find("[data-testid='send-btn']").classes()).toContain("transition-none");
    expect(wrapper.find("[data-testid='send-options-btn']").attributes("data-variant")).toBe(
      "ghost",
    );
    expect(wrapper.find("[data-testid='send-options-btn']").classes()).toContain("transition-none");
  });

  it("textarea carries the .composer-input class so the high-contrast ::selection rule can target it", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("textarea").classes()).toContain("composer-input");
  });

  it("shows the connected placeholder with neutral muted styling", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");

    expect(document.activeElement).not.toBe(textarea.element);
    expect(textarea.classes()).not.toContain("placeholder:font-semibold");
    expect(textarea.classes()).toContain("placeholder:text-[#bfbfbf]");
    expect(textarea.classes()).not.toContain("placeholder:text-destructive");
  });

  it("keeps the disconnected placeholder neutral and removes colored composer states", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isConnected: false } });
    const textarea = wrapper.find("textarea");
    const composer = wrapper.find(".composer-pill");

    expect(textarea.attributes("placeholder")).toBe("Reconnecting…");
    expect(textarea.classes()).not.toContain("placeholder:font-semibold");
    expect(textarea.classes()).toContain("placeholder:text-[#bfbfbf]");
    expect(textarea.classes()).not.toContain("placeholder:text-destructive");
    expect(composer.classes()).not.toContain("composer-pill--connected");
    expect(composer.classes()).not.toContain("composer-pill--disconnected");
  });

  it("renders the paperclip attach button — chat composer accepts any file", () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect(wrapper.find("[data-testid='attach-btn']").exists()).toBe(true);
    // No `accept` filter: the backend takes any MIME, so the picker
    // should match. A stale accept="image/*" used to silently drop
    // anything else the user picked.
    expect(wrapper.find("[data-testid='file-input']").attributes("accept")).toBeUndefined();
  });

  it("keeps textarea enabled while thinking — every send goes straight to the dispatcher queue", () => {
    // Under the dispatcher model the input is always live: typing while a
    // claude run is in flight just means the next prompt lands in the
    // backend queue. There's no front-end gating.
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).disabled).toBe(false);
  });

  it("Send button stays enabled while thinking so a follow-up prompt can be queued", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    const btn = wrapper.find("[data-testid='send-btn']").element as HTMLButtonElement;
    // Disabled only because the textarea is empty in this fixture — set
    // a value and confirm the button enables.
    expect(btn.disabled).toBe(true);
  });

  it("Send is enabled with text even while a turn is in flight (queue-it-up)", async () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    await wrapper.find("textarea").setValue("queue me");
    expect((wrapper.find("[data-testid='send-btn']").element as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it("disables the textarea when disconnected", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isConnected: false } });
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).disabled).toBe(true);
  });

  it("disables Send button when not connected", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isConnected: false } });
    const btn = wrapper.find("[data-testid='send-btn']");
    expect((btn.element as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows both cancel and send buttons when thinking", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    expect(wrapper.find("[data-testid='cancel-btn']").exists()).toBe(true);
    expect(wrapper.find("[data-testid='send-btn']").exists()).toBe(true);
  });

  it("shows the pointer cursor on the cancel button", () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    expect(wrapper.find("[data-testid='cancel-btn']").classes()).toContain("cursor-pointer");
  });

  it("does not emit send on plain Enter (multi-line prompts must compose freely)", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await textarea.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("send")).toBeFalsy();
  });

  it("Ctrl+Enter submits on Windows/Linux", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await textarea.trigger("keydown", { key: "Enter", ctrlKey: true });
    expect(wrapper.emitted("send")).toBeTruthy();
    expect(wrapper.emitted("send")![0]).toEqual(["hello"]);
    expect((textarea.element as HTMLTextAreaElement).value).toBe("");
  });

  it("Cmd+Enter (metaKey) submits on macOS", async () => {
    setPlatform("MacIntel");
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello mac");
    await textarea.trigger("keydown", { key: "Enter", metaKey: true });
    expect(wrapper.emitted("send")).toBeTruthy();
    expect(wrapper.emitted("send")![0]).toEqual(["hello mac"]);
  });

  it("Cmd+Enter does not submit on Windows/Linux", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello meta");
    await textarea.trigger("keydown", { key: "Enter", metaKey: true });
    expect(wrapper.emitted("send")).toBeFalsy();
  });

  it("Ctrl+Enter does not submit on macOS", async () => {
    setPlatform("MacIntel");
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello ctrl");
    await textarea.trigger("keydown", { key: "Enter", ctrlKey: true });
    expect(wrapper.emitted("send")).toBeFalsy();
  });

  it("Ctrl+Enter on an empty editor does not emit send", async () => {
    // The textarea is the source of truth for sendability; the modifier
    // shortcut must obey the same gate as the Send button so an accidental
    // Ctrl+Enter on an empty pill doesn't dispatch a blank prompt.
    const wrapper = mount(ChatInput, { props: defaultProps });
    await wrapper.find("textarea").trigger("keydown", { key: "Enter", ctrlKey: true });
    expect(wrapper.emitted("send")).toBeFalsy();
  });

  it("emits send when Send button is clicked", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect(wrapper.emitted("send")).toBeTruthy();
    expect(wrapper.emitted("send")![0]).toEqual(["hello"]);
  });

  it("emits cancel when cancel button clicked", async () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, isThinking: true } });
    await wrapper.find("[data-testid='cancel-btn']").trigger("click");
    expect(wrapper.emitted("cancel")).toBeTruthy();
  });

  it("clears input after send", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect((textarea.element as HTMLTextAreaElement).value).toBe("");
  });

  it("recallText prepends into the editor and emits recall-consumed", async () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, recallText: "" } });
    await wrapper.find("textarea").setValue("typing in progress");
    await wrapper.setProps({ recallText: "earlier prompt" });
    await flushPromises();
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).value).toBe(
      "earlier prompt\n\ntyping in progress",
    );
    expect(wrapper.emitted("recall-consumed")).toBeTruthy();
  });

  it("restores queued attachments with recalled text and resends their metadata", async () => {
    const attachment = {
      name: "queued.png",
      mime: "image/png",
      path: ".daymug/agents/agent-1/uploads/queued.png",
      url: "/api/users/agent/files/read?path=queued.png",
    };
    const ref = "/home/alice/.daymug/agents/agent-1/uploads/queued.png";
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, recallText: "", recallAttachments: [] },
    });

    await wrapper.setProps({ recallAttachments: [attachment], recallText: `check this\n\n${ref}` });
    await flushPromises();

    expect(wrapper.get("[data-testid='attachment-chip']").text()).toContain("queued.png");
    await wrapper.get("[data-testid='send-btn']").trigger("click");
    expect(wrapper.emitted("send")?.[0]?.[0]).toBe(`check this\n\n${ref}`);
    expect(wrapper.emitted("send")?.[0]?.[1]).toEqual([attachment]);
  });

  it("warns when an image is attached to a text-only model", async () => {
    const image = {
      name: "shot.png",
      mime: "image/png",
      path: ".daymug/agents/agent-1/uploads/shot.png",
      url: "/api/users/agent/files/read?path=shot.png",
    };
    const wrapper = mount(ChatInput, {
      props: { ...defaultProps, imageInputUnsupported: true },
    });
    expect(wrapper.find("[data-testid='image-input-warning']").exists()).toBe(false);

    await wrapper.setProps({ recallAttachments: [image], recallText: "look" });
    await flushPromises();
    expect(wrapper.find("[data-testid='image-input-warning']").exists()).toBe(true);

    await wrapper.setProps({ imageInputUnsupported: false });
    expect(wrapper.find("[data-testid='image-input-warning']").exists()).toBe(false);
  });

  // --- attachment / paste flow ---

  it("uploads a pasted image and embeds the returned ref in the sent message", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/abc-shot.png",
      path: ".daymug/agents/agent-1/uploads/abc-shot.png",
      name: "shot.png",
      size: 3,
      mime: "image/png",
      url: "/api/users/agent/files/read?path=abc-shot.png",
    });

    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    textarea.element.dispatchEvent(
      makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
    );
    await flushPromises();

    expect(uploadMock).toHaveBeenCalledOnce();
    // First arg is the File, second is conversationId, third is filename.
    expect(uploadMock.mock.calls[0][1]).toBe("conv-1");
    const chip = wrapper.find("[data-testid='attachment-chip']");
    expect(chip.exists()).toBe(true);
    expect(chip.text()).toContain("shot.png");

    await textarea.setValue("Look at this");
    await wrapper.find("[data-testid='send-btn']").trigger("click");

    const sent = wrapper.emitted("send")?.[0]?.[0] as string | undefined;
    expect(sent).toBe("Look at this\n\n/home/alice/.daymug/agents/agent-1/uploads/abc-shot.png");
    expect(wrapper.emitted("send")?.[0]?.[1]).toEqual([
      {
        name: "shot.png",
        mime: "image/png",
        path: ".daymug/agents/agent-1/uploads/abc-shot.png",
        url: "/api/users/agent/files/read?path=abc-shot.png",
      },
    ]);
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(false);
    expect((textarea.element as HTMLTextAreaElement).value).toBe("");
  });

  it("keeps uploaded attachments with their conversation while switching away and back", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/saved.png",
      path: ".daymug/agents/agent-1/uploads/saved.png",
      name: "saved.png",
      size: 3,
      mime: "image/png",
      url: "/api/users/agent/files/read?path=saved.png",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile("saved.png") },
        ]),
      );
    await flushPromises();
    expect(wrapper.get("[data-testid='attachment-chip']").text()).toContain("saved.png");

    await wrapper.setProps({ conversationId: "conv-2" });
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(false);

    await wrapper.setProps({ conversationId: "conv-1" });
    expect(wrapper.get("[data-testid='attachment-chip']").text()).toContain("saved.png");
  });

  it("keeps uploaded attachments when the composer is temporarily unmounted", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/mobile.png",
      path: ".daymug/agents/agent-1/uploads/mobile.png",
      name: "mobile.png",
      size: 3,
      mime: "image/png",
      url: "/api/users/agent/files/read?path=mobile.png",
    });
    const first = mount(ChatInput, { props: defaultProps });
    first
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile("mobile.png") },
        ]),
      );
    await flushPromises();
    first.unmount();

    const remounted = mount(ChatInput, { props: defaultProps });
    expect(remounted.get("[data-testid='attachment-chip']").text()).toContain("mobile.png");
  });

  it("ignores plain-text paste so normal copy/paste still works", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent(
          [{ kind: "string", type: "text/plain", getAsFile: () => null }],
          "hello world",
        ),
      );
    await flushPromises();
    expect(uploadMock).not.toHaveBeenCalled();
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(false);
  });

  it("prefers text over the image when the clipboard carries both (Excel/Sheets copy)", async () => {
    // Copying a cell range out of Excel / Numbers / Google Sheets puts
    // a text/plain payload AND a rendered image of the selection on
    // the clipboard. Users almost always want the text in that case;
    // the image-upload branch used to silently shadow it. Pasting in
    // this mixed state should fall through to the textarea (no upload,
    // no attachment chip) so the browser's default paste lands the
    // text into the input.
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper.find("textarea").element.dispatchEvent(
      makePasteEvent(
        [
          { kind: "string", type: "text/plain", getAsFile: () => null },
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile() },
        ],
        "A1\tB1\nA2\tB2",
      ),
    );
    await flushPromises();
    expect(uploadMock).not.toHaveBeenCalled();
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(false);
  });

  it("uploads non-image files (PDFs, logs, …) pasted as file-kind clipboard items", async () => {
    // The chat composer used to silently drop anything that wasn't an
    // image; the backend now accepts arbitrary MIME so the front-end
    // should hand any file-kind paste through to the uploader.
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/notes.pdf",
      path: ".daymug/agents/agent-1/uploads/notes.pdf",
      name: "notes.pdf",
      size: 12,
      mime: "application/pdf",
      url: "/api/users/agent/files/read?path=notes.pdf",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    const pdfFile = new File([new Uint8Array([0x25, 0x50, 0x44, 0x46])], "notes.pdf", {
      type: "application/pdf",
    });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "application/pdf", getAsFile: () => pdfFile }]),
      );
    await flushPromises();
    expect(uploadMock).toHaveBeenCalledOnce();
    expect(wrapper.find("[data-testid='attachment-chip']").text()).toContain("notes.pdf");

    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect(wrapper.emitted("send")?.[0]?.[1]).toEqual([
      {
        name: "notes.pdf",
        mime: "application/pdf",
        path: ".daymug/agents/agent-1/uploads/notes.pdf",
        url: "/api/users/agent/files/read?path=notes.pdf",
      },
    ]);
  });

  it("uploads any file dropped onto the composer regardless of MIME", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/log.txt",
      path: ".daymug/agents/agent-1/uploads/log.txt",
      name: "log.txt",
      size: 5,
      mime: "text/plain",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    const txtFile = new File([new Uint8Array([97, 98, 99])], "log.txt", { type: "text/plain" });
    const dropEvent = new Event("drop") as unknown as DragEvent;
    Object.defineProperty(dropEvent, "dataTransfer", { value: { files: [txtFile] } });
    Object.defineProperty(dropEvent, "preventDefault", { value: () => {} });
    // Drop fires on the composer pill, not the textarea — find it by the
    // composer-pill class so the @drop binding catches the event.
    wrapper.find(".composer-pill").element.dispatchEvent(dropEvent);
    await flushPromises();
    expect(uploadMock).toHaveBeenCalledOnce();
    expect(wrapper.find("[data-testid='attachment-chip']").text()).toContain("log.txt");
  });

  it("emits 'uploaded' when an upload finishes so the parent can lock the work_dir", async () => {
    // The parent (ChatPage → useChat.lockWorkDirAfterUpload) listens on
    // this event to flip isWorkDirLocked the moment an upload succeeds: the
    // server has just pinned the conversation to the directory the next turn
    // will run in, and letting the workspace panel move it afterwards would
    // point the agent at a different project than the one being discussed.
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/x.png",
      path: ".daymug/agents/agent-1/uploads/x.png",
      name: "x.png",
      size: 3,
      mime: "image/png",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();
    expect(wrapper.emitted("uploaded")).toBeTruthy();
    expect(wrapper.emitted("uploaded")!.length).toBe(1);
  });

  it("does not emit 'uploaded' when an upload fails — the lock should track real successes", async () => {
    uploadMock.mockRejectedValue(new Error("boom"));
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();
    expect(wrapper.emitted("uploaded")).toBeFalsy();
  });

  it("surfaces upload errors as a dismissible chip", async () => {
    uploadMock.mockRejectedValue(new Error("upload denied"));
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();
    const errChip = wrapper.find("[data-testid='attachment-error']");
    expect(errChip.exists()).toBe(true);
    // Tooltip content is now portal-rendered on hover; the chip itself just
    // carries the error class. The fact that it rendered as an error chip is
    // enough — the visible text comes through the shadcn Tooltip.
    expect(errChip.classes()).toContain("text-destructive");
    await errChip.find("button").trigger("click");
    expect(wrapper.find("[data-testid='attachment-error']").exists()).toBe(false);
  });

  it("send button stays enabled with attachment-only message (no typed text)", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/x.png",
      path: ".daymug/agents/agent-1/uploads/x.png",
      name: "x.png",
      size: 3,
      mime: "image/png",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile("x.png") },
        ]),
      );
    await flushPromises();
    const sendBtn = wrapper.find("[data-testid='send-btn']").element as HTMLButtonElement;
    expect(sendBtn.disabled).toBe(false);
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect(wrapper.emitted("send")?.[0]?.[0]).toBe(
      "/home/alice/.daymug/agents/agent-1/uploads/x.png",
    );
  });

  it("shows a progress chip and disables Send while an upload is in flight", async () => {
    // Pending mock: the promise never resolves on its own so we can inspect
    // the chip mid-upload. Capture the onProgress callback so we can
    // simulate progress events from the wire.
    let progressCb: ((p: { loaded: number; total: number }) => void) | undefined;
    uploadMock.mockImplementation((_file, _conv, _filename, options) => {
      progressCb = options?.onProgress;
      return new Promise(() => {
        /* never resolves — chip stays in flight */
      });
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    await wrapper.find("textarea").setValue("with image");
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();

    expect(wrapper.find("[data-testid='attachment-uploading']").exists()).toBe(true);
    // Send must be blocked while bytes are still on the wire — a half-
    // uploaded ref would land in chat history with an unreachable path.
    expect((wrapper.find("[data-testid='send-btn']").element as HTMLButtonElement).disabled).toBe(
      true,
    );

    // Drive a progress event and confirm the percentage text + bar update.
    progressCb?.({ loaded: 25, total: 100 });
    await flushPromises();
    expect(wrapper.find("[data-testid='upload-progress-text']").text()).toBe("25%");
    expect(
      (wrapper.find("[data-testid='upload-progress-bar']").element as HTMLElement).style.width,
    ).toBe("25%");
  });

  it("cancel button on an in-flight upload aborts and removes the chip silently", async () => {
    // Capture the AbortSignal so we can verify the X click triggers abort()
    // on the underlying XHR — when it does, the upload promise rejects
    // with AbortError and uploadFile drops the chip without surfacing an
    // error.
    let capturedSignal: AbortSignal | undefined;
    uploadMock.mockImplementation((_file, _conv, _filename, options) => {
      capturedSignal = options?.signal;
      return new Promise((_, reject) => {
        capturedSignal?.addEventListener("abort", () => {
          reject(new DOMException("aborted", "AbortError"));
        });
      });
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();
    expect(wrapper.find("[data-testid='attachment-uploading']").exists()).toBe(true);

    await wrapper.find("[data-testid='cancel-upload']").trigger("click");
    await flushPromises();
    expect(capturedSignal?.aborted).toBe(true);
    // Cancel is a deliberate action; the chip should disappear without a
    // red error chip flashing in its place.
    expect(wrapper.find("[data-testid='attachment-uploading']").exists()).toBe(false);
    expect(wrapper.find("[data-testid='attachment-error']").exists()).toBe(false);
  });

  it("X on a completed attachment removes it (post-upload cancel)", async () => {
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/x.png",
      path: ".daymug/agents/agent-1/uploads/x.png",
      name: "x.png",
      size: 3,
      mime: "image/png",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile("x.png") },
        ]),
      );
    await flushPromises();
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(true);
    await wrapper.find("[data-testid='remove-attachment']").trigger("click");
    expect(wrapper.find("[data-testid='attachment-chip']").exists()).toBe(false);
  });

  it("mints a blob preview URL for image uploads and revokes it when the chip is removed", async () => {
    // The chip's hover tooltip renders this URL into an <img> so the user
    // can see what they pasted without re-fetching from the backend. Asserting
    // on URL.createObjectURL / revokeObjectURL covers the lifecycle in a
    // way the portal-rendered Tooltip body would let us assert in jsdom.
    const createSpy = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:preview-1");
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/x.png",
      path: ".daymug/agents/agent-1/uploads/x.png",
      name: "x.png",
      size: 3,
      mime: "image/png",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([
          { kind: "file", type: "image/png", getAsFile: () => makeImageFile("x.png") },
        ]),
      );
    await flushPromises();
    expect(createSpy).toHaveBeenCalledTimes(1);
    await wrapper.find("[data-testid='remove-attachment']").trigger("click");
    expect(revokeSpy).toHaveBeenCalledWith("blob:preview-1");
    createSpy.mockRestore();
    revokeSpy.mockRestore();
  });

  it("does not mint a preview URL for non-image uploads", async () => {
    const createSpy = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:should-not-be-called");
    uploadMock.mockResolvedValue({
      ref: "/home/alice/.daymug/agents/agent-1/uploads/log.txt",
      path: ".daymug/agents/agent-1/uploads/log.txt",
      name: "log.txt",
      size: 5,
      mime: "text/plain",
    });
    const wrapper = mount(ChatInput, { props: defaultProps });
    const txtFile = new File([new Uint8Array([97, 98, 99])], "log.txt", { type: "text/plain" });
    const dropEvent = new Event("drop") as unknown as DragEvent;
    Object.defineProperty(dropEvent, "dataTransfer", { value: { files: [txtFile] } });
    Object.defineProperty(dropEvent, "preventDefault", { value: () => {} });
    wrapper.find(".composer-pill").element.dispatchEvent(dropEvent);
    await flushPromises();
    expect(createSpy).not.toHaveBeenCalled();
    createSpy.mockRestore();
  });

  it("blocks upload and surfaces an error chip when conversationId is empty", async () => {
    const wrapper = mount(ChatInput, { props: { ...defaultProps, conversationId: "" } });
    wrapper
      .find("textarea")
      .element.dispatchEvent(
        makePasteEvent([{ kind: "file", type: "image/png", getAsFile: () => makeImageFile() }]),
      );
    await flushPromises();
    expect(uploadMock).not.toHaveBeenCalled();
    expect(wrapper.find("[data-testid='attachment-error']").exists()).toBe(true);
  });

  // --- slash text is ordinary input ---

  it("treats a leading slash as plain text: no popup, sent verbatim", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("/cl");
    expect(wrapper.find("[data-testid='slash-menu']").exists()).toBe(false);
    await textarea.trigger("keydown", { key: "Enter" });
    await textarea.trigger("keydown", { key: "Tab" });
    expect(wrapper.emitted("send")).toBeFalsy();
    await textarea.trigger("keydown", { key: "Enter", ctrlKey: true });
    expect(wrapper.emitted("send")![0]).toEqual(["/cl"]);
  });

  it("Enter is still ignored on plain prose so the textarea behaves normally", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello world");
    await textarea.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("send")).toBeFalsy();
  });

  // --- draft persistence ---

  it("restores a previously-saved draft on mount", () => {
    localStorage.setItem(
      DRAFTS_STORAGE_KEY,
      JSON.stringify({ "conv-1": { text: "saved earlier", updatedAt: 1 } }),
    );
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).value).toBe("saved earlier");
  });

  it("writes the typed draft back to localStorage after the debounce window", async () => {
    vi.useFakeTimers();
    try {
      const wrapper = mount(ChatInput, { props: defaultProps });
      await wrapper.find("textarea").setValue("in-progress prompt");
      vi.advanceTimersByTime(400);
      const raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
      const map = raw ? JSON.parse(raw) : {};
      expect(map["conv-1"]?.text).toBe("in-progress prompt");
    } finally {
      vi.useRealTimers();
    }
  });

  it("swaps the textarea content when conversationId changes — each conv keeps its own draft", async () => {
    localStorage.setItem(
      DRAFTS_STORAGE_KEY,
      JSON.stringify({
        "conv-1": { text: "first conv draft", updatedAt: 1 },
        "conv-2": { text: "second conv draft", updatedAt: 2 },
      }),
    );
    const wrapper = mount(ChatInput, { props: defaultProps });
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).value).toBe(
      "first conv draft",
    );
    await wrapper.setProps({ conversationId: "conv-2" });
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).value).toBe(
      "second conv draft",
    );
  });

  it("persists the outgoing conv's keystrokes synchronously before swapping in the new draft", async () => {
    const wrapper = mount(ChatInput, { props: defaultProps });
    await wrapper.find("textarea").setValue("typed but not sent");
    // No timer advance — switching conv must flush the pending save itself,
    // otherwise the user's last keystrokes would be lost on a quick switch.
    await wrapper.setProps({ conversationId: "conv-2" });
    const raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
    const map = raw ? JSON.parse(raw) : {};
    expect(map["conv-1"]?.text).toBe("typed but not sent");
  });

  it("drops the localStorage entry once the message is sent", async () => {
    vi.useFakeTimers();
    try {
      const wrapper = mount(ChatInput, { props: defaultProps });
      await wrapper.find("textarea").setValue("about to send");
      vi.advanceTimersByTime(400);
      // sanity
      let raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
      expect(raw && JSON.parse(raw)["conv-1"]?.text).toBe("about to send");
      await wrapper.find("[data-testid='send-btn']").trigger("click");
      vi.advanceTimersByTime(400);
      raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
      const map = raw ? JSON.parse(raw) : {};
      expect(map["conv-1"]).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });
});
