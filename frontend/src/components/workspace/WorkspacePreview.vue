<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

import { isEditableText, shouldInspectTextFile, useFileApi } from "@/composables/useFileApi";
import { officeAppFor } from "@/lib/office/files";
import FilePreview from "@/components/FilePreview.vue";
import FileIcon from "@/components/FileIcon.vue";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { X, RefreshCw } from "lucide-vue-next";

const { t } = useI18n();
const { downloadFileUrl, inspectFile } = useFileApi();

const props = defineProps<{
  userId: string;
  // Workspace-relative path of the previewed file.
  path: string;
}>();

const emit = defineEmits<{
  close: [];
  // Open the file in the editor (new tab) — the parent owns the router
  // navigation so this stays a dumb overlay.
  edit: [path: string];
}>();

// Preview surface — exposes refresh() so the modal header's reload button can
// re-fetch the same path without forcing the user to close + reopen.
const filePreviewRef = ref<{ refresh: () => void } | null>(null);

function refreshPreview() {
  filePreviewRef.value?.refresh();
}

const probedText = ref(false);
let probeRun = 0;
const canEdit = computed(
  () =>
    isEditableText(props.path.split("/").pop() ?? "") ||
    officeAppFor(props.path) !== null ||
    probedText.value,
);

watch(
  () => props.path,
  async (path) => {
    const run = ++probeRun;
    probedText.value = false;
    if (!shouldInspectTextFile(path)) return;
    try {
      const info = await inspectFile(props.userId, path);
      if (run === probeRun) probedText.value = info.is_text;
    } catch {
      if (run === probeRun) probedText.value = false;
    }
  },
  { immediate: true },
);

// The overlay is view-only; editing (text or office) always happens in a
// dedicated window opened by the parent, never inline in the popup.
function handleEdit() {
  emit("edit", props.path);
}
</script>

<template>
  <!-- File preview — modal-centered overlay (UI design `FilePreviewOverlay`).
       Backdrop blur + 1100×720 paper card with rounded corners. Click on
       the backdrop to close; Escape is handled by the parent panel, which
       owns the route-backed open/close state. -->
  <Teleport to="body">
    <div
      class="fixed inset-0 z-[999] flex items-center justify-center md:p-8 bg-foreground/55 backdrop-blur-md"
      @click.self="emit('close')"
    >
      <div
        class="ws-preview-modal flex flex-col bg-card border-border overflow-hidden w-full h-full md:border md:rounded-xl md:shadow-2xl"
      >
        <header class="flex items-center gap-3 px-5 py-3.5 border-b border-border shrink-0">
          <FileIcon
            :is-dir="false"
            :file-name="path.split('/').pop() ?? ''"
            :size="28"
            class="text-muted-foreground"
          />
          <div class="flex-1 min-w-0">
            <div class="font-mono text-[13px] font-semibold text-foreground truncate">
              <span v-if="path.includes('/')" class="text-muted-foreground">{{
                path.split("/").slice(0, -1).join("/") + "/"
              }}</span>
              <span>{{ path.split("/").pop() }}</span>
            </div>
            <div class="text-[11px] text-muted-foreground font-mono mt-0.5">
              {{ t("workspace.preview") }}
            </div>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <Button
              variant="ghost"
              size="sm"
              class="ws-preview-refresh h-7 px-2"
              :title="t('workspace.refreshPreview')"
              @click="refreshPreview"
            >
              <RefreshCw class="size-4" />
            </Button>
            <Button
              v-if="canEdit"
              variant="outline"
              size="sm"
              class="h-7 px-3 text-xs"
              :title="t('files.preview.openInEditor')"
              @click="handleEdit"
              >{{ t("common.edit") }}</Button
            >
            <Button variant="outline" size="sm" class="h-7 px-3 text-xs" as-child>
              <a :href="downloadFileUrl(props.userId, path)" download>{{ t("common.download") }}</a>
            </Button>
            <Button
              variant="ghost"
              size="sm"
              class="h-7 px-2"
              :title="t('workspace.closePreview')"
              @click="emit('close')"
            >
              <X class="size-4" />
            </Button>
          </div>
        </header>
        <FilePreview
          ref="filePreviewRef"
          :user-id="props.userId"
          :path="path"
          class="flex-1 min-h-0"
        />
        <footer
          class="flex items-center gap-3 px-5 h-8 border-t border-border bg-[var(--paper-alt)] font-mono text-[10px] text-muted-foreground shrink-0"
        >
          <span class="inline-flex items-center gap-1.5"
            ><Kbd>Esc</Kbd> {{ t("workspace.escClose") }}</span
          >
          <span class="flex-1" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* Preview modal: fullscreen on mobile, capped paper-card on desktop. The
   inline style approach used to clamp width/height directly, which
   couldn't be overridden by responsive utilities; switching to a media
   query keeps the desktop cap while letting mobile take the whole
   viewport (matches the `md:p-8` / `md:rounded-xl` switch on the
   wrapper). */
@media (min-width: 768px) {
  .ws-preview-modal {
    width: min(1100px, 100%);
    height: min(720px, 100%);
  }
}
</style>
