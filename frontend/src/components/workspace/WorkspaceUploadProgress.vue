<script setup lang="ts">
// Upload progress bar pinned to the bottom of the workspace panel while an
// upload is in flight. Speed sits in the upper-right above a thin green bar so
// the user can glance at throughput without losing the file count. The
// trailing Cancel button aborts the current per-file POST: files already
// uploaded stay, the in-flight file's partial body is dropped server-side,
// queued files never start.
import { useI18n } from "vue-i18n";
import type { UploadProgressState } from "@/composables/useWorkspaceUpload";
import { Button } from "@/components/ui/button";
import { formatBytes, formatSpeed } from "@/lib/uploadHelpers";

defineProps<{ progress: UploadProgressState; cancelled: boolean }>();
defineEmits<{ cancel: [] }>();

const { t } = useI18n();
</script>

<template>
  <div
    class="ws-upload-progress shrink-0 px-3 py-2 bg-card border-t border-border"
    data-testid="workspace-upload-progress"
  >
    <div
      class="flex items-center justify-between gap-3 mb-1.5 text-[11px] font-mono text-muted-foreground"
    >
      <span class="truncate">
        <template v-if="cancelled">{{ t("workspace.uploadCancelling") }}</template>
        <template v-else>
          {{
            t("workspace.uploadProgress", {
              done: progress.doneCount,
              count: progress.fileCount,
              loaded: formatBytes(progress.loaded),
              total: formatBytes(progress.total),
            })
          }}
        </template>
      </span>
      <span class="flex items-center gap-2 shrink-0">
        <span class="tabular-nums">{{ formatSpeed(progress.speed) }}</span>
        <Button
          variant="ghost"
          size="sm"
          class="ws-upload-cancel h-5 px-2 text-[11px]"
          data-testid="workspace-upload-cancel"
          :disabled="cancelled"
          @click="$emit('cancel')"
        >
          {{ t("workspace.uploadCancel") }}
        </Button>
      </span>
    </div>
    <div class="w-full h-1.5 rounded-full overflow-hidden bg-green-100">
      <div
        class="h-full bg-green-500 transition-[width] duration-100 ease-out"
        :style="{
          width: `${
            progress.total > 0 ? Math.min(100, (progress.loaded / progress.total) * 100) : 0
          }%`,
        }"
      />
    </div>
  </div>
</template>
