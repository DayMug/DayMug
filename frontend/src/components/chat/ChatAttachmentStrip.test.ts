import { describe, it, expect } from "vitest";
import { defineComponent } from "vue";
import { mount } from "@vue/test-utils";

import ChatAttachmentStrip from "./ChatAttachmentStrip.vue";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { Attachment, Upload } from "@/composables/useChatAttachments";

const attachment = (over: Partial<Attachment> = {}): Attachment =>
  ({
    uploadId: 1,
    name: "shot.png",
    ref: "./up/shot.png",
    mime: "image/png",
    ...over,
  }) as Attachment;

const upload = (over: Partial<Upload> = {}): Upload => ({
  uploadId: 2,
  name: "big.zip",
  status: "uploading",
  progress: 0.4,
  ...over,
});

// The chips are Tooltip triggers, so they need a provider in scope — the real
// composer supplies one at the top of ChatInput.
const Host = defineComponent({
  components: { ChatAttachmentStrip, TooltipProvider },
  props: {
    attachments: { type: Array as () => Attachment[], default: () => [] },
    uploads: { type: Array as () => Upload[], default: () => [] },
  },
  template: `
    <TooltipProvider>
      <ChatAttachmentStrip :attachments="attachments" :uploads="uploads" />
    </TooltipProvider>`,
});

function mountStrip(props: { attachments?: Attachment[]; uploads?: Upload[] } = {}) {
  const wrapper = mount(Host, { props });
  return { wrapper, strip: wrapper.findComponent(ChatAttachmentStrip) };
}

describe("ChatAttachmentStrip", () => {
  it("stays out of the DOM when there is nothing to show", () => {
    const { wrapper } = mountStrip();
    expect(wrapper.find('[data-testid="attachment-strip"]').exists()).toBe(false);
  });

  it("renders a chip per finished attachment", () => {
    const { wrapper } = mountStrip({ attachments: [attachment()] });
    expect(wrapper.find('[data-testid="attachment-chip"]').text()).toContain("shot.png");
  });

  it("shows upload progress as text and as a bar", () => {
    const { wrapper } = mountStrip({ uploads: [upload({ progress: 0.42 })] });
    expect(wrapper.find('[data-testid="upload-progress-text"]').text()).toBe("42%");
    expect(wrapper.find('[data-testid="upload-progress-bar"]').attributes("style")).toContain(
      "width: 42%",
    );
  });

  it("marks a failed upload distinctly from an in-flight one", () => {
    const { wrapper } = mountStrip({ uploads: [upload({ status: "error", error: "disk full" })] });
    expect(wrapper.find('[data-testid="attachment-error"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="attachment-uploading"]').exists()).toBe(false);
  });

  it("only offers cancel while an upload is in flight", () => {
    expect(
      mountStrip({ uploads: [upload()] })
        .wrapper.find('[data-testid="cancel-upload"]')
        .exists(),
    ).toBe(true);
    expect(
      mountStrip({ uploads: [upload({ status: "error" })] })
        .wrapper.find('[data-testid="cancel-upload"]')
        .exists(),
    ).toBe(false);
  });

  it("reports a cancelled upload by id", async () => {
    const { wrapper, strip } = mountStrip({ uploads: [upload({ uploadId: 7 })] });
    await wrapper.find('[data-testid="cancel-upload"]').trigger("click");
    expect(strip.emitted("cancel-upload")?.[0]).toEqual([7]);
  });

  it("reports a dismissed error chip by id", async () => {
    const { wrapper, strip } = mountStrip({
      uploads: [upload({ uploadId: 8, status: "error" })],
    });
    await wrapper.find('[data-testid="attachment-error"] button').trigger("click");
    expect(strip.emitted("dismiss-upload")?.[0]).toEqual([8]);
  });

  it("reports a removed attachment by id", async () => {
    const { wrapper, strip } = mountStrip({ attachments: [attachment({ uploadId: 9 })] });
    await wrapper.find('[data-testid="remove-attachment"]').trigger("click");
    expect(strip.emitted("remove-attachment")?.[0]).toEqual([9]);
  });
});
