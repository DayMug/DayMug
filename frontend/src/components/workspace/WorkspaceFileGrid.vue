<script setup lang="ts">
// Finder-style tile grid for the workspace panel. Purely a renderer: every
// piece of state it reads and every handler it calls comes from the panel's
// entry context.
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import type { FileEntry } from "@/composables/useFileApi";
import FileIcon from "../FileIcon.vue";
import FileName from "../FileName.vue";
import { useWorkspaceEntryContext } from "./workspaceEntryContext";
import { formatBytes } from "@/lib/uploadHelpers";

defineProps<{ entries: FileEntry[] }>();

const { t } = useI18n();
const ctx = useWorkspaceEntryContext();

// The ref sits inside v-for, so Vue 3 stores it as an array even though the
// surrounding v-if guarantees at most one rendered element at a time.
const renameInput = ref<HTMLInputElement | HTMLInputElement[]>();

function focusRenameInput() {
  const r = renameInput.value;
  const el = Array.isArray(r) ? r[0] : r;
  el?.focus();
  el?.select();
}

defineExpose({ focusRenameInput });
</script>

<template>
  <div class="ws-grid grid gap-[9px] p-4 pt-0">
    <div
      v-for="entry in entries"
      :key="entry.name"
      :data-entry-name="entry.name"
      data-cursor-surface="pointer"
      class="file-card flex min-h-[108px] cursor-pointer select-none flex-col items-center justify-center gap-1.5 rounded-lg border border-[#e9e9e9] bg-white px-2.5 py-2.5 transition-colors dark:border-[#292929] dark:bg-[#1b1b1b]"
      :class="{
        'selected bg-[var(--accent-soft)] outline outline-[1.5px] -outline-offset-[1.5px] outline-primary':
          ctx.selectedEntries.value.has(entry.name),
        'is-cut opacity-45': ctx.cutNames.value.has(entry.name),
        'outline outline-[1.5px] -outline-offset-[1.5px] outline-primary bg-[var(--accent-soft)]':
          entry.is_dir && ctx.dragOverTarget.value === entry.name,
        'is-dragging opacity-50': ctx.draggedEntry.value?.name === entry.name,
        'hover:border-[#d9d9d9] hover:bg-[#fdfdfd] dark:hover:border-[#3a3a3a] dark:hover:bg-[#202020]':
          !ctx.selectedEntries.value.has(entry.name) &&
          !(entry.is_dir && ctx.dragOverTarget.value === entry.name),
      }"
      :draggable="ctx.renamingEntry.value !== entry.name"
      @click="ctx.onEntryClick(entry, $event)"
      @dblclick="ctx.onEntryDblClick(entry, $event)"
      @contextmenu.stop="ctx.onEntryContextMenu($event, entry)"
      @pointerdown.stop="ctx.startLongPress($event, entry, ctx.getEntryPath(entry))"
      @pointermove="ctx.moveLongPress"
      @pointerup="ctx.endLongPress"
      @pointercancel="ctx.endLongPress"
      @dragstart="ctx.onEntryDragStart($event, entry)"
      @dragend="ctx.onEntryDragEnd"
      @dragover.prevent.stop="entry.is_dir && ctx.onFolderDragOver($event, entry.name)"
      @dragleave="entry.is_dir && ctx.onFolderDragLeave(entry.name)"
      @drop.prevent.stop="entry.is_dir && ctx.onFolderDrop($event, entry.name)"
    >
      <div class="relative inline-flex shrink-0 text-foreground">
        <FileIcon :is-dir="entry.is_dir" :file-name="entry.name" :size="24" />
      </div>
      <template v-if="ctx.renamingEntry.value === entry.name">
        <input
          ref="renameInput"
          v-model="ctx.renameValue.value"
          class="w-full min-w-0 text-[11px] text-center px-1 py-px border border-ring rounded bg-background text-foreground outline-none box-border"
          @compositionstart="ctx.renameComposing.value = true"
          @compositionend="ctx.renameComposing.value = false"
          @keydown.enter="ctx.onRenameEnter"
          @keydown.escape="ctx.cancelRename"
          @blur="ctx.confirmRename"
          @click.stop
        />
      </template>
      <FileName
        v-else
        :name="entry.name"
        class="file-name w-full justify-center text-[13px] leading-snug"
        :class="[
          'font-sans',
          ctx.selectedEntries.value.has(entry.name)
            ? 'font-semibold text-foreground'
            : 'text-foreground',
        ]"
        :title="entry.name"
      />
      <span class="text-[11px] text-[#999999] dark:text-muted-foreground">
        {{ entry.is_dir ? t("files.menu.folder") : formatBytes(entry.size) }}
      </span>
    </div>
  </div>
</template>

<style scoped>
/* iPad/iOS Safari fires `contextmenu` on long-press, but it ALSO opens the
   system selection callout (copy / share / look up) on top of our menu.
   Suppress the callout on the surfaces that own a contextmenu handler so
   the long-press gesture lands cleanly on FileContextMenu instead. */
.file-card {
  -webkit-touch-callout: none;
}

/* Responsive file grid: smaller cards on narrow viewports */
.ws-grid {
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
}
@media (max-width: 480px) {
  .ws-grid {
    grid-template-columns: repeat(auto-fill, minmax(72px, 1fr));
  }
}
</style>
