<script setup lang="ts">
// Chip row above the composer pill. In-flight and failed uploads sit next to
// the finished attachments so a failure is visible right where the chip that
// produced it was. Presentational: all lifecycle lives in useChatAttachments.
import { useI18n } from "vue-i18n";
import { X, Paperclip, Image as ImageIcon } from "lucide-vue-next";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { isImageMime, type Attachment, type Upload } from "@/composables/useChatAttachments";

defineProps<{ attachments: Attachment[]; uploads: Upload[] }>();

const emit = defineEmits<{
  "cancel-upload": [uploadId: number];
  "dismiss-upload": [uploadId: number];
  "remove-attachment": [uploadId: number];
}>();

const { t } = useI18n();

function percent(progress: number): number {
  return Math.round(progress * 100);
}
</script>

<template>
  <div
    v-if="attachments.length > 0 || uploads.length > 0"
    class="flex flex-wrap gap-1 mb-2"
    data-testid="attachment-strip"
  >
    <Tooltip v-for="u in uploads" :key="`u-${u.uploadId}`">
      <TooltipTrigger as-child>
        <div
          class="relative flex items-center gap-1.5 px-2 py-1 border border-border rounded text-xs overflow-hidden"
          :class="
            u.status === 'error'
              ? 'bg-destructive/10 text-destructive'
              : 'bg-muted text-muted-foreground'
          "
          :data-testid="u.status === 'error' ? 'attachment-error' : 'attachment-uploading'"
        >
          <Paperclip :size="12" />
          <span class="max-w-[12rem] truncate">{{ u.name }}</span>
          <span
            v-if="u.status === 'uploading'"
            class="text-[10px] font-mono tabular-nums shrink-0"
            data-testid="upload-progress-text"
            >{{ percent(u.progress) }}%</span
          >
          <button
            v-if="u.status === 'uploading'"
            class="text-muted-foreground hover:text-foreground shrink-0"
            :aria-label="t('chat.cancelUpload')"
            data-testid="cancel-upload"
            @click.stop="emit('cancel-upload', u.uploadId)"
          >
            <X :size="12" />
          </button>
          <button
            v-if="u.status === 'error'"
            class="hover:opacity-70 shrink-0"
            :aria-label="t('chat.dismiss')"
            @click.stop="emit('dismiss-upload', u.uploadId)"
          >
            <X :size="12" />
          </button>
          <!-- Progress bar along the chip's bottom edge, sized off the same
               `progress` field the percentage text reads so the two can't
               disagree. -->
          <div
            v-if="u.status === 'uploading'"
            class="absolute left-0 bottom-0 h-0.5 bg-primary/70 transition-[width] duration-150"
            :style="{ width: `${percent(u.progress)}%` }"
            data-testid="upload-progress-bar"
          ></div>
        </div>
      </TooltipTrigger>
      <TooltipContent side="top" :class="u.previewUrl ? 'p-1 max-w-none rounded-md' : ''">
        <img
          v-if="u.previewUrl"
          :src="u.previewUrl"
          :alt="u.name"
          class="block max-w-[20rem] max-h-[20rem] object-contain rounded"
          data-testid="attachment-preview-image"
        />
        <template v-else>{{
          u.status === "error"
            ? u.error || t("chat.uploadFailed")
            : t("chat.uploadingProgress", { percent: percent(u.progress) })
        }}</template>
      </TooltipContent>
    </Tooltip>
    <Tooltip v-for="a in attachments" :key="`a-${a.uploadId}`">
      <TooltipTrigger as-child>
        <div
          class="flex items-center gap-1.5 px-2 py-1 border border-border rounded text-xs bg-card text-foreground"
          data-testid="attachment-chip"
        >
          <ImageIcon v-if="isImageMime(a.mime)" :size="12" />
          <Paperclip v-else :size="12" />
          <span class="max-w-[12rem] truncate">{{ a.name }}</span>
          <button
            class="text-muted-foreground hover:text-foreground shrink-0"
            :aria-label="t('chat.removeAttachment')"
            data-testid="remove-attachment"
            @click.stop="emit('remove-attachment', a.uploadId)"
          >
            <X :size="12" />
          </button>
        </div>
      </TooltipTrigger>
      <TooltipContent side="top" :class="a.previewUrl ? 'p-1 max-w-none rounded-md' : ''">
        <img
          v-if="a.previewUrl"
          :src="a.previewUrl"
          :alt="a.name"
          class="block max-w-[20rem] max-h-[20rem] object-contain rounded"
          data-testid="attachment-preview-image"
        />
        <template v-else>{{ a.ref }}</template>
      </TooltipContent>
    </Tooltip>
  </div>
</template>
