import { describe, it, expect, vi, beforeEach } from "vitest";
import { ref } from "vue";
import { mount, flushPromises } from "@vue/test-utils";

import { useChatAttachments, isImageMime } from "./useChatAttachments";

const { uploadMock } = vi.hoisted(() => ({ uploadMock: vi.fn() }));
vi.mock("@/composables/useApi", () => ({ uploadFile: uploadMock }));

const createObjectURL = vi.fn(() => "blob:preview");
const revokeObjectURL = vi.fn();

function host(convId = ref("c1")) {
  let api: ReturnType<typeof useChatAttachments>;
  const wrapper = mount({
    setup() {
      api = useChatAttachments(convId);
      return () => null;
    },
  });
  return { api: api!, wrapper };
}

function imageFile(name = "shot.png") {
  return new File(["x"], name, { type: "image/png" });
}

beforeEach(() => {
  vi.clearAllMocks();
  createObjectURL.mockReturnValue("blob:preview");
  vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
  uploadMock.mockResolvedValue({ ref: "./up/shot.png", name: "shot.png", mime: "image/png" });
});

describe("isImageMime", () => {
  it("recognises image MIME types only", () => {
    expect(isImageMime("image/png")).toBe(true);
    expect(isImageMime("application/pdf")).toBe(false);
    expect(isImageMime(undefined)).toBe(false);
  });
});

describe("useChatAttachments", () => {
  it("refuses to upload before a conversation is picked", async () => {
    const { api } = host(ref(""));
    await api.uploadFile(imageFile());

    expect(uploadMock).not.toHaveBeenCalled();
    expect(api.uploads.value[0].status).toBe("error");
  });

  it("promotes a finished upload into an attachment chip", async () => {
    const { api } = host();
    await api.uploadFile(imageFile());

    expect(api.uploads.value).toHaveLength(0);
    expect(api.attachments.value[0].ref).toBe("./up/shot.png");
  });

  it("mints a hover preview for images but not other files", async () => {
    const { api } = host();
    await api.uploadFile(new File(["x"], "notes.txt", { type: "text/plain" }));
    expect(createObjectURL).not.toHaveBeenCalled();

    await api.uploadFile(imageFile());
    expect(createObjectURL).toHaveBeenCalledOnce();
  });

  it("surfaces a failed upload as an error chip", async () => {
    uploadMock.mockRejectedValueOnce(new Error("disk full"));
    const { api } = host();
    await api.uploadFile(imageFile());

    expect(api.uploads.value[0]).toMatchObject({ status: "error", error: "disk full" });
  });

  it("drops a cancelled upload silently instead of flagging an error", async () => {
    uploadMock.mockRejectedValueOnce(new DOMException("aborted", "AbortError"));
    const { api } = host();
    await api.uploadFile(imageFile());

    expect(api.uploads.value).toHaveLength(0);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });

  it("aborts the in-flight request when the chip is cancelled", async () => {
    let signal: AbortSignal | undefined;
    uploadMock.mockImplementation(
      (_f: File, _c: string, _n: string, opts: { signal: AbortSignal }) => {
        signal = opts.signal;
        return new Promise(() => {});
      },
    );
    const { api } = host();
    void api.uploadFile(imageFile());
    await flushPromises();

    api.cancelUpload(api.uploads.value[0].uploadId);
    expect(signal?.aborted).toBe(true);
  });

  it("reports in-flight uploads so the composer can block sending", async () => {
    uploadMock.mockImplementation(() => new Promise(() => {}));
    const { api } = host();
    void api.uploadFile(imageFile());
    await flushPromises();

    expect(api.isUploading()).toBe(true);
  });

  it("revokes the preview when an attachment is removed", async () => {
    const { api } = host();
    await api.uploadFile(imageFile());
    revokeObjectURL.mockClear();

    api.removeAttachment(api.attachments.value[0].uploadId);
    expect(api.attachments.value).toHaveLength(0);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });

  it("revokes the preview when an error chip is dismissed", async () => {
    uploadMock.mockRejectedValueOnce(new Error("nope"));
    const { api } = host();
    await api.uploadFile(imageFile());
    revokeObjectURL.mockClear();

    api.dismissUpload(api.uploads.value[0].uploadId);
    expect(api.uploads.value).toHaveLength(0);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });

  it("clears attachments and error chips after a send", async () => {
    const { api } = host();
    await api.uploadFile(imageFile());
    uploadMock.mockRejectedValueOnce(new Error("nope"));
    await api.uploadFile(imageFile("bad.png"));

    api.clearAfterSend();
    expect(api.attachments.value).toHaveLength(0);
    expect(api.uploads.value).toHaveLength(0);
  });

  it("keeps an in-flight upload alive across a send", async () => {
    const { api } = host();
    uploadMock.mockImplementation(() => new Promise(() => {}));
    void api.uploadFile(imageFile());
    await flushPromises();

    api.clearAfterSend();
    expect(api.uploads.value).toHaveLength(1);
  });

  // The chip leaves the composer but its blob URL must not: the draft is
  // stashed so switching back restores the same attachment, preview included.
  it("stashes attachments when the conversation changes, keeping their previews alive", async () => {
    const convId = ref("c1");
    const { api } = host(convId);
    await api.uploadFile(imageFile());
    revokeObjectURL.mockClear();

    convId.value = "c2";
    await flushPromises();

    expect(api.attachments.value).toHaveLength(0);
    expect(revokeObjectURL).not.toHaveBeenCalled();

    convId.value = "c1";
    await flushPromises();

    expect(api.attachments.value).toHaveLength(1);
    expect(api.attachments.value[0].previewUrl).toBe("blob:preview");
  });

  it("aborts an in-flight upload when the conversation changes", async () => {
    let signal: AbortSignal | undefined;
    uploadMock.mockImplementation(
      (_f: File, _c: string, _n: string, opts: { signal: AbortSignal }) => {
        signal = opts.signal;
        return new Promise(() => {});
      },
    );
    const convId = ref("c1");
    const { api } = host(convId);
    void api.uploadFile(imageFile());
    await flushPromises();

    convId.value = "c2";
    await flushPromises();

    expect(signal?.aborted).toBe(true);
    expect(api.uploads.value).toHaveLength(0);
  });

  it("discards an upload that lands after the conversation changed", async () => {
    let resolveUpload: (r: unknown) => void = () => {};
    uploadMock.mockImplementation(() => new Promise((r) => (resolveUpload = r)));
    const convId = ref("c1");
    const { api } = host(convId);
    void api.uploadFile(imageFile());
    await flushPromises();

    convId.value = "c2";
    await flushPromises();
    // The response for conversation c1 arrives only now, while c2 is bound.
    resolveUpload({ ref: "./up/shot.png", name: "shot.png", mime: "image/png" });
    await flushPromises();

    expect(api.attachments.value).toHaveLength(0);
  });

  it("releases in-flight upload previews on unmount but keeps stashed attachments", async () => {
    const { api, wrapper } = host();
    await api.uploadFile(imageFile());
    revokeObjectURL.mockClear();

    wrapper.unmount();
    // A completed upload has become a stashed attachment, and the draft store
    // hands it back on the next mount — revoking here would restore a chip
    // whose preview is already dead.
    expect(revokeObjectURL).not.toHaveBeenCalled();
  });
});
