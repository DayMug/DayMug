<script setup lang="ts">
// Upload panel pinned to the bottom of the workspace panel. While a run is in
// flight, speed sits in the upper-right above a thin green bar and the Cancel
// button aborts the current per-file POST: files already uploaded stay, the
// in-flight file's partial body is dropped server-side, queued files never
// start. The header expands upward into a per-file list of outcomes; it opens
// by itself when a file fails. The panel stays after the run ends — the user
// has to see which files failed or were skipped — until closed with the X.
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  ChevronDown,
  ChevronUp,
  CircleCheck,
  CircleSlash,
  CircleX,
  LoaderCircle,
  X,
} from "lucide-vue-next";
import type { UploadProgressState, UploadResult } from "@/composables/useWorkspaceUpload";
import { Button } from "@/components/ui/button";
import { formatBytes, formatSpeed } from "@/lib/uploadHelpers";

const props = defineProps<{
  progress: UploadProgressState | null;
  results: UploadResult[];
  cancelled: boolean;
}>();
defineEmits<{ cancel: []; dismiss: [] }>();

const { t } = useI18n();

const counts = computed(() => {
  const c = { done: 0, failed: 0, skipped: 0, cancelled: 0 };
  for (const r of props.results) {
    if (r.status === "done") c.done++;
    else if (r.status === "failed") c.failed++;
    else if (r.status === "skipped") c.skipped++;
    else if (r.status === "cancelled") c.cancelled++;
  }
  return c;
});

const expanded = ref(false);
watch(
  () => counts.value.failed,
  (failed, before) => {
    if (failed > (before ?? 0)) expanded.value = true;
  },
  { immediate: true },
);

function statusText(r: UploadResult): string {
  if (r.status === "done" && r.resolved) return t(`workspace.uploadStatus.${r.resolved}`);
  return t(`workspace.uploadStatus.${r.status}`);
}
</script>

<template>
  <div
    class="ws-upload-progress shrink-0 px-3 py-2 bg-card border-t border-border"
    data-testid="workspace-upload-progress"
  >
    <ul
      v-if="expanded && results.length"
      class="mb-2 max-h-[40vh] overflow-y-auto space-y-0.5 text-[11px]"
      data-testid="workspace-upload-results"
    >
      <li
        v-for="r in results"
        :key="r.id"
        class="ws-upload-result flex items-center gap-1.5 min-w-0"
        :data-status="r.status"
      >
        <CircleCheck v-if="r.status === 'done'" class="size-3.5 shrink-0 text-green-600" />
        <CircleX v-else-if="r.status === 'failed'" class="size-3.5 shrink-0 text-destructive" />
        <LoaderCircle
          v-else-if="r.status === 'uploading'"
          class="size-3.5 shrink-0 animate-spin text-muted-foreground"
        />
        <CircleSlash v-else class="size-3.5 shrink-0 text-muted-foreground" />
        <span class="truncate text-foreground" :title="r.name">{{ r.name }}</span>
        <span
          class="ml-auto shrink-0 max-w-[50%] truncate"
          :class="r.status === 'failed' ? 'text-destructive' : 'text-muted-foreground'"
          :title="r.error"
        >
          {{ r.status === "failed" && r.error ? r.error : statusText(r) }}
        </span>
      </li>
    </ul>
    <div
      class="flex items-center justify-between gap-3 text-[11px] font-mono text-muted-foreground"
      :class="{ 'mb-1.5': progress }"
    >
      <button
        type="button"
        class="ws-upload-toggle flex items-center gap-1 min-w-0 hover:text-foreground"
        data-testid="workspace-upload-toggle"
        :aria-expanded="expanded"
        :title="t(expanded ? 'workspace.uploadHideList' : 'workspace.uploadShowList')"
        @click="expanded = !expanded"
      >
        <ChevronDown v-if="expanded" class="size-3.5 shrink-0" />
        <ChevronUp v-else class="size-3.5 shrink-0" />
        <span class="truncate">
          <template v-if="!progress">
            {{ t("workspace.uploadFinished", counts) }}
          </template>
          <template v-else-if="cancelled">{{ t("workspace.uploadCancelling") }}</template>
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
      </button>
      <span class="flex items-center gap-2 shrink-0">
        <template v-if="progress">
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
        </template>
        <Button
          v-else
          variant="ghost"
          size="icon"
          class="size-5"
          data-testid="workspace-upload-dismiss"
          :title="t('workspace.uploadDismiss')"
          :aria-label="t('workspace.uploadDismiss')"
          @click="$emit('dismiss')"
        >
          <X class="size-3.5" />
        </Button>
      </span>
    </div>
    <div v-if="progress" class="w-full h-1.5 rounded-full overflow-hidden bg-green-100">
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
