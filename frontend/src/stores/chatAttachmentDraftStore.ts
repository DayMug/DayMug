// Uploaded attachments that have not been sent yet, keyed by conversation.
// ChatInput is reused while switching conversations and can also be unmounted
// temporarily by the mobile files tab, so the composer itself cannot be the
// only owner of these drafts.
import type { UploadResult } from "@/composables/useApi";

export type AttachmentDraft = UploadResult & { uploadId: number; previewUrl?: string };

const drafts = new Map<string, AttachmentDraft[]>();

// Draft ownership moves between the mounted composer and this store. Taking a
// draft removes it from the map so a blob preview URL always has one owner.
export function takeAttachmentDraft(conversationId: string): AttachmentDraft[] {
  if (!conversationId) return [];
  const draft = drafts.get(conversationId) ?? [];
  drafts.delete(conversationId);
  return draft;
}

export function stashAttachmentDraft(conversationId: string, attachments: AttachmentDraft[]): void {
  if (!conversationId) return;
  discardAttachmentDraft(conversationId);
  if (attachments.length === 0) return;
  drafts.set(conversationId, attachments);
}

export function discardAttachmentDraft(conversationId: string): void {
  const draft = drafts.get(conversationId);
  if (!draft) return;
  for (const attachment of draft) {
    if (attachment.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
  }
  drafts.delete(conversationId);
}

export function resetChatAttachmentDraftStore(): void {
  for (const conversationId of drafts.keys()) discardAttachmentDraft(conversationId);
}
