<script setup lang="ts">
// List view for the workspace panel — a lazy file tree. Purely a renderer:
// the tree state comes in through the `tree` prop (owned by the panel so it
// survives grid/list toggles) and everything else through the entry context.
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { ChevronRight, LoaderCircle } from "lucide-vue-next";
import type { WorkspaceTree, WorkspaceTreeRow } from "@/composables/useWorkspaceTree";
import FileIcon from "../FileIcon.vue";
import FileName from "../FileName.vue";
import { useWorkspaceEntryContext } from "./workspaceEntryContext";
import { formatBytes } from "@/lib/uploadHelpers";

defineProps<{ tree: WorkspaceTree }>();

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

// Where a drop on this row goes. A top-level folder is keyed by name like the
// grid; an expanded subfolder by its path; a nested file drops into the folder
// it sits in. A top-level file has no target: the drop bubbles to the panel
// body and lands in the current folder.
function dropTarget(row: WorkspaceTreeRow): { key: string; path?: string } | null {
  if (row.depth === 0) return row.entry.is_dir ? { key: row.entry.name } : null;
  if (row.entry.is_dir) return { key: row.path, path: row.path };
  const parent = row.path.slice(0, row.path.lastIndexOf("/"));
  return { key: parent, path: parent };
}

function isDropHighlighted(row: WorkspaceTreeRow): boolean {
  const target = ctx.dragOverTarget.value;
  if (!target || !row.entry.is_dir) return false;
  // A nested file keys its top-level parent by path, so match both forms.
  return target === row.path || (row.depth === 0 && target === row.entry.name);
}

function onDragOver(event: DragEvent, row: WorkspaceTreeRow) {
  const target = dropTarget(row);
  if (target) ctx.onFolderDragOver(event, target.key, target.path);
}

function onDragLeave(row: WorkspaceTreeRow) {
  const target = dropTarget(row);
  if (target) ctx.onFolderDragLeave(target.key);
}

function onDrop(event: DragEvent, row: WorkspaceTreeRow) {
  const target = dropTarget(row);
  if (target) ctx.onFolderDrop(event, target.key, target.path);
}
</script>

<template>
  <div class="ws-list flex flex-col gap-[5px] p-4 pt-0" role="tree">
    <div
      v-for="row in tree.treeRows.value"
      :key="row.path"
      :data-entry-name="row.depth === 0 ? row.entry.name : undefined"
      :data-tree-path="row.path"
      data-cursor-surface="pointer"
      class="file-row flex min-h-12 cursor-pointer select-none items-center gap-2.5 rounded-lg border pr-2.5 transition-colors"
      :class="{
        'selected bg-[var(--accent-soft)]':
          row.depth === 0
            ? ctx.selectedEntries.value.has(row.entry.name)
            : ctx.selectedTreePath.value === row.path,
        'is-cut opacity-45': row.depth === 0 && ctx.cutNames.value.has(row.entry.name),
        'outline outline-[1.5px] outline-primary bg-[var(--accent-soft)]': isDropHighlighted(row),
        'is-dragging opacity-50':
          row.depth === 0 && ctx.draggedEntry.value?.name === row.entry.name,
        'border-[#e9e9e9] bg-white hover:border-[#d8d8d8] dark:border-[#292929] dark:bg-[#1b1b1b]':
          row.depth === 0,
        'border-transparent': row.depth > 0,
        'hover:bg-[var(--paper-alt)]':
          row.depth > 0 &&
          (row.depth > 0 || !ctx.selectedEntries.value.has(row.entry.name)) &&
          !isDropHighlighted(row),
      }"
      :style="{ paddingLeft: `${10 + row.depth * 18}px` }"
      :draggable="row.depth === 0 && ctx.renamingEntry.value !== row.entry.name"
      role="treeitem"
      :aria-level="row.depth + 1"
      :aria-expanded="row.entry.is_dir ? tree.expandedTreePaths.value.has(row.path) : undefined"
      @click="ctx.onEntryClick(row.entry, $event, row.path)"
      @dblclick="ctx.onEntryDblClick(row.entry, $event, row.path)"
      @contextmenu.stop="ctx.onEntryContextMenu($event, row.entry, row.path)"
      @pointerdown.stop="ctx.startLongPress($event, row.entry, row.path)"
      @pointermove="ctx.moveLongPress"
      @pointerup="ctx.endLongPress"
      @pointercancel="ctx.endLongPress"
      @dragstart="row.depth === 0 && ctx.onEntryDragStart($event, row.entry)"
      @dragend="row.depth === 0 && ctx.onEntryDragEnd()"
      @dragover="onDragOver($event, row)"
      @dragleave="onDragLeave(row)"
      @drop="onDrop($event, row)"
    >
      <div class="relative inline-flex shrink-0 text-foreground">
        <FileIcon
          :is-dir="row.entry.is_dir"
          :file-name="row.entry.name"
          :size="19"
          :expanded="tree.expandedTreePaths.value.has(row.path)"
        />
      </div>
      <template v-if="row.depth === 0 && ctx.renamingEntry.value === row.entry.name">
        <input
          ref="renameInput"
          v-model="ctx.renameValue.value"
          class="flex-1 min-w-0 text-[12px] px-1 py-px border border-ring rounded bg-background text-foreground outline-none box-border"
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
        :name="row.entry.name"
        class="file-name min-w-0 flex-1 font-sans text-[13px]"
        :class="[
          (
            row.depth === 0
              ? ctx.selectedEntries.value.has(row.entry.name)
              : ctx.selectedTreePath.value === row.path
          )
            ? 'font-semibold text-foreground'
            : 'text-foreground',
        ]"
        :title="row.path"
      />
      <span
        v-if="row.depth === 0"
        class="shrink-0 text-[11px] text-[#999999] dark:text-muted-foreground"
      >
        {{ row.entry.is_dir ? t("files.menu.folder") : formatBytes(row.entry.size) }}
      </span>
      <button
        v-if="row.entry.is_dir"
        type="button"
        class="tree-toggle inline-flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :class="{ 'text-destructive': tree.treeLoadErrors.value.has(row.path) }"
        :title="tree.treeLoadErrors.value.get(row.path) ?? row.entry.name"
        :aria-label="row.entry.name"
        @click.stop="tree.toggleTreeDirectory(row.path)"
        @dblclick.stop
      >
        <LoaderCircle
          v-if="tree.loadingTreePaths.value.has(row.path)"
          class="size-3.5 animate-spin"
        />
        <ChevronRight
          v-else
          class="size-3.5 transition-transform"
          :class="{ 'rotate-90': tree.expandedTreePaths.value.has(row.path) }"
        />
      </button>
    </div>
  </div>
</template>

<style scoped>
/* iPad/iOS Safari fires `contextmenu` on long-press, but it ALSO opens the
   system selection callout (copy / share / look up) on top of our menu.
   Suppress the callout on the surfaces that own a contextmenu handler so
   the long-press gesture lands cleanly on FileContextMenu instead. */
.file-row {
  -webkit-touch-callout: none;
}
</style>
