import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import { useI18n } from "vue-i18n";
import { uploadFile as uploadFileApi } from "@/composables/useApi";
import type { MessageAttachment } from "@/composables/useWebSocket";
import {
  stashAttachmentDraft,
  takeAttachmentDraft,
  type AttachmentDraft,
} from "@/stores/chatAttachmentDraftStore";
import { errorMessage } from "@/lib/errorMessage";

// Uploaded but not yet sent. `previewUrl` is a blob: URL minted via
// URL.createObjectURL for image uploads so the chip's hover tooltip can show
// the picture without a backend round-trip.
//
// Blob-URL ownership is the delicate part of this module: whoever drops an
// entry must revoke its URL. A conversation switch or unmount transfers that
// ownership to chatAttachmentDraftStore instead of discarding the entry.
export type Attachment = AttachmentDraft;

// Per-upload status surfaced as a transient chip. Kept separate from
// `attachments` because a failed upload has no `ref` to embed in a message.
export type Upload = {
  uploadId: number;
  name: string;
  status: "uploading" | "error";
  /** 0-1 fraction driving the per-chip progress bar. */
  progress: number;
  error?: string;
  /** Held so the chip's X can tear down the XHR mid-flight. */
  controller?: AbortController;
  previewUrl?: string;
};

export interface ChatAttachments {
  attachments: Ref<Attachment[]>;
  uploads: Ref<Upload[]>;
  isUploading: () => boolean;
  uploadFile: (file: File, filename?: string) => Promise<void>;
  cancelUpload: (uploadId: number) => void;
  removeAttachment: (uploadId: number) => void;
  dismissUpload: (uploadId: number) => void;
  restoreAttachments: (attachments: MessageAttachment[]) => void;
  /** Drop every attachment and error chip after a successful send. */
  clearAfterSend: () => void;
}

export function isImageMime(mime: string | undefined): boolean {
  return !!mime && mime.startsWith("image/");
}

// useChatAttachments owns the composer's upload lifecycle: in-flight progress,
// cancellation, failure chips, and the blob URLs backing hover previews.
export function useChatAttachments(conversationId: Ref<string>): ChatAttachments {
  const { t } = useI18n();

  const initialAttachments = takeAttachmentDraft(conversationId.value);
  const attachments = ref<Attachment[]>(initialAttachments);
  const uploads = ref<Upload[]>([]);
  let seq = initialAttachments.reduce((max, attachment) => Math.max(max, attachment.uploadId), 0);
  // Generation token for the conversation the composer is currently bound to.
  // Every upload captures it before its await; switching conversations bumps
  // it, which turns any response that lands afterwards into a no-op.
  let conversationRun = 0;

  function isUploading(): boolean {
    return uploads.value.some((u) => u.status === "uploading");
  }

  async function uploadFile(file: File, filename?: string) {
    const id = ++seq;
    const run = conversationRun;
    const displayName = filename ?? file.name ?? "file";
    if (!conversationId.value) {
      uploads.value.push({
        uploadId: id,
        name: displayName,
        status: "error",
        progress: 0,
        error: t("chat.pickConversationBeforeUpload"),
      });
      return;
    }
    // Mint the preview from the local File so the picture is visible
    // immediately — including while the upload is still in flight. Ownership
    // transfers to the attachment entry on success; every failure path revokes.
    const previewUrl = file.type.startsWith("image/") ? URL.createObjectURL(file) : undefined;
    const controller = new AbortController();
    uploads.value.push({
      uploadId: id,
      name: displayName,
      status: "uploading",
      progress: 0,
      controller,
      previewUrl,
    });
    try {
      const res = await uploadFileApi(file, conversationId.value, filename, {
        signal: controller.signal,
        onProgress: ({ loaded, total }) => {
          const idx = uploads.value.findIndex((u) => u.uploadId === id);
          if (idx >= 0 && total > 0) {
            uploads.value[idx] = { ...uploads.value[idx], progress: loaded / total };
          }
        },
      });
      // The user switched conversations while this was in flight: the chips
      // (and their blob URLs) are already gone, and promoting the result here
      // would attach the file to the wrong conversation's draft.
      if (run !== conversationRun) return;
      // Drop the in-flight chip and add the durable one in the same tick, so
      // the user sees a single state rather than a flicker.
      uploads.value = uploads.value.filter((u) => u.uploadId !== id);
      attachments.value.push({ ...res, uploadId: id, previewUrl });
    } catch (e) {
      // Same staleness rule as the success path — including the abort the
      // conversation switch itself raised. The switch already revoked this
      // upload's preview URL, so there is nothing left to clean up here.
      if (run !== conversationRun) return;
      // User-initiated cancel: drop the chip silently. Surfacing AbortError as
      // a red error chip would mis-frame an intentional click.
      if (e instanceof DOMException && e.name === "AbortError") {
        uploads.value = uploads.value.filter((u) => u.uploadId !== id);
        if (previewUrl) URL.revokeObjectURL(previewUrl);
        return;
      }
      const msg = errorMessage(e);
      const idx = uploads.value.findIndex((u) => u.uploadId === id);
      if (idx >= 0) {
        uploads.value[idx] = {
          uploadId: id,
          name: displayName,
          status: "error",
          progress: 0,
          error: msg,
          previewUrl,
        };
      }
    }
  }

  // abort() raises the XHR's "abort" event, which routes to the AbortError
  // branch above and removes the chip — no splice needed here.
  function cancelUpload(uploadId: number) {
    uploads.value.find((x) => x.uploadId === uploadId)?.controller?.abort();
  }

  function removeAttachment(uploadId: number) {
    const a = attachments.value.find((x) => x.uploadId === uploadId);
    if (a?.previewUrl) URL.revokeObjectURL(a.previewUrl);
    attachments.value = attachments.value.filter((x) => x.uploadId !== uploadId);
  }

  function dismissUpload(uploadId: number) {
    const u = uploads.value.find((x) => x.uploadId === uploadId);
    if (u?.previewUrl) URL.revokeObjectURL(u.previewUrl);
    uploads.value = uploads.value.filter((x) => x.uploadId !== uploadId);
  }

  function restoreAttachments(restored: MessageAttachment[]) {
    if (restored.length === 0) return;
    // Recalled prompt text already carries the absolute attachment refs, so a
    // restored chip must not append a second ref when it is sent again.
    attachments.value.unshift(
      ...restored.map((attachment) => ({
        ...attachment,
        ref: "",
        size: 0,
        uploadId: ++seq,
      })),
    );
  }

  function clearAfterSend() {
    for (const a of attachments.value) {
      if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
    }
    attachments.value = [];
    // Dropped error chips own preview URLs too (image upload that failed
    // mid-flight); revoke before pruning so we don't leak blobs.
    for (const u of uploads.value) {
      if (u.status === "error" && u.previewUrl) URL.revokeObjectURL(u.previewUrl);
    }
    uploads.value = uploads.value.filter((u) => u.status !== "error");
  }

  // An attachment is scoped to the conversation it was uploaded into: the
  // backend stored it under that conversation's work_dir, so carrying the chip
  // across a switch would hand the next agent a path outside its sandbox.
  // The composer component is reused (not re-keyed) across conversations, so
  // this watcher — not unmount — is the only point where that reset can happen.
  watch(conversationId, (next, previous) => {
    conversationRun++;
    for (const u of uploads.value) u.controller?.abort();
    for (const u of uploads.value) {
      if (u.previewUrl) URL.revokeObjectURL(u.previewUrl);
    }
    stashAttachmentDraft(previous, attachments.value);
    const nextAttachments = takeAttachmentDraft(next);
    for (const attachment of nextAttachments) seq = Math.max(seq, attachment.uploadId);
    attachments.value = nextAttachments;
    uploads.value = [];
  });

  // Browsers eventually GC blob URLs on unload, but explicit revocation keeps
  // memory pressure predictable and avoids surprising the next mount.
  onBeforeUnmount(() => {
    for (const u of uploads.value) u.controller?.abort();
    for (const u of uploads.value) {
      if (u.previewUrl) URL.revokeObjectURL(u.previewUrl);
    }
    stashAttachmentDraft(conversationId.value, attachments.value);
  });

  return {
    attachments,
    uploads,
    isUploading,
    uploadFile,
    cancelUpload,
    removeAttachment,
    dismissUpload,
    restoreAttachments,
    clearAfterSend,
  };
}
