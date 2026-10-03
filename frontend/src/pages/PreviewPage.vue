<script setup lang="ts">
import { computed, ref, watch, watchEffect } from "vue";
import { useRoute, useRouter } from "vue-router";

import FileWorkbench from "@/components/FileWorkbench.vue";
import WorkspacePanel from "@/components/WorkspacePanel.vue";
import { useResizablePanels } from "@/composables/useResizablePanels";

// Route-binding shell around FileWorkbench. Everything about *how* a file is
// shown lives in the workbench; this page owns only the URL it is driven by
// and the workspace panel beside it.
const props = defineProps<{
  userId: string;
}>();

const route = useRoute();
const router = useRouter();
const containerRef = ref<HTMLElement>();

const filePath = computed(() => (typeof route.query.path === "string" ? route.query.path : ""));
const fileName = computed(() => filePath.value.split("/").pop() ?? "");
const fileDir = computed(() => {
  const slash = filePath.value.lastIndexOf("/");
  return slash >= 0 ? filePath.value.slice(0, slash) : "";
});

// The panel is rooted at the folder of the file the page opened with, and
// stays there. Following every same-tab file switch would yank the root out
// from under the user: list view opens files from nested folders it lazily
// expanded, and either view lets them browse away from the current file's
// folder before opening something. Only a different agent re-seeds it.
const filePanelPath = ref(fileDir.value || ".");
watch([() => props.userId, filePath], ([userId], [prevUserId, prevPath]) => {
  if (userId === prevUserId && prevPath !== "") return;
  filePanelPath.value = fileDir.value || ".";
});

function isEditQuery(value: unknown): boolean {
  return value === "true" || value === "1";
}

const editMode = computed(() => isEditQuery(route.query.edit));

// A content-search hit knows which line matched; carry it through the URL so
// a reload (or a shared link) lands in the same place the search did.
const targetLine = computed(() => {
  const raw = route.query.line;
  const n = typeof raw === "string" ? Number.parseInt(raw, 10) : NaN;
  return Number.isFinite(n) && n > 0 ? n : 0;
});

function onOpen(payload: { path: string; line: number }) {
  const query: Record<string, string> = { ...(route.query as Record<string, string>) };
  query.path = payload.path;
  if (payload.line > 0) query.line = String(payload.line);
  else delete query.line;
  void router.push({ query });
}

// Keep the URL's `edit` flag in sync with the mode the workbench resolved, so
// a copied or reloaded link reopens the same way.
function onEditModeChange(on: boolean) {
  const edit = on ? "true" : "false";
  if (route.query.edit === edit) return;
  void router.replace({ query: { ...route.query, edit } });
}

const {
  isCollapsed: isFilePanelCollapsed,
  isDragging,
  workspacePanelStyle: filePanelStyle,
  onDragStart,
  onTouchStart,
  collapse: collapseFilePanel,
  expand: expandFilePanel,
} = useResizablePanels(containerRef, {
  storageKey: "daymug.preview.filePanelWidth",
  collapseStorageKey: "daymug.preview.filePanelCollapsed",
  defaultWidth: 340,
  minWidth: 200,
  maxWidth: 640,
});

watchEffect(() => {
  if (fileName.value) {
    document.title = fileName.value;
  }
});
</script>

<template>
  <div class="h-screen bg-background">
    <div
      ref="containerRef"
      class="relative flex h-full min-h-0"
      :class="{ 'select-none': isDragging }"
      data-testid="preview-layout"
    >
      <FileWorkbench
        :user-id="props.userId"
        :path="filePath"
        :edit-mode="editMode"
        :reveal-line="targetLine"
        storage-scope="preview"
        enable-quick-open
        :show-panel-expand="isFilePanelCollapsed"
        @open="onOpen"
        @update:edit-mode="onEditModeChange"
        @expand-panel="expandFilePanel"
      />

      <div
        v-if="filePath && !isFilePanelCollapsed"
        class="resize-handle group relative hidden w-px shrink-0 bg-border cursor-col-resize transition-colors hover:bg-primary/30 md:block"
        :class="{ 'bg-primary/30': isDragging }"
        style="touch-action: none"
        data-testid="preview-file-panel-resize-handle"
        @mousedown="onDragStart"
        @touchstart="onTouchStart"
      >
        <span
          class="absolute inset-y-0 left-1/2 z-10 w-3 -translate-x-1/2 touch-none"
          aria-hidden="true"
        />
      </div>

      <aside
        v-if="filePath"
        class="absolute inset-y-0 right-0 z-30 shrink-0 border-l border-border bg-background shadow-xl md:relative md:z-auto md:border-l-0 md:shadow-none"
        :style="{ ...filePanelStyle, maxWidth: '85vw' }"
        data-testid="preview-file-panel"
      >
        <WorkspacePanel
          :user-id="props.userId"
          :initial-path="filePanelPath"
          can-collapse
          file-open-target="same-tab"
          @collapse="collapseFilePanel"
        />
      </aside>
    </div>
  </div>
</template>
