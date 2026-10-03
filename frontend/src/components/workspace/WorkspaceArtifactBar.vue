<script setup lang="ts">
import { FolderOpen, Maximize2, Minimize2, PanelRight, Plus, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";

import FileIcon from "../FileIcon.vue";

defineProps<{
  paths: string[];
  activePath: string | null;
  directoryOpen: boolean;
  fullscreen: boolean;
  canCollapse?: boolean;
}>();

const emit = defineEmits<{
  activate: [path: string];
  close: [path: string];
  directory: [];
  "toggle-fullscreen": [];
  collapse: [];
}>();

const { t } = useI18n();

function nameOf(path: string): string {
  return path.split("/").pop() || path;
}
</script>

<template>
  <!-- Browser-style tab strip: tabs are rounded-top cards that sit *on* the
       strip and merge into the pane below when active, so the active tab and
       its content read as one surface. While the directory browser is open no
       tab owns the pane, so the whole strip flattens onto the browser's white
       background and every tab renders unselected. -->
  <div
    class="artifact-tabs flex h-12 shrink-0 items-stretch border-b border-[#e6e6e6] pt-[5px] dark:border-border"
    :class="directoryOpen ? 'bg-card dark:bg-[#151515]' : 'bg-[#f6f6f6] dark:bg-[#151515]'"
    data-testid="artifact-tabs"
    :data-directory-open="directoryOpen"
  >
    <div
      class="artifact-tab-scroll flex min-w-0 flex-1 items-stretch gap-px overflow-x-auto px-[5px]"
    >
      <button
        v-for="path in paths"
        :key="path"
        type="button"
        class="group/tab flex min-w-[105px] max-w-[145px] shrink-0 items-center gap-[5px] rounded-t-lg border border-b-0 border-transparent px-[9px] text-[11px] text-[#74767c] transition-colors dark:text-muted-foreground"
        :class="
          !directoryOpen && activePath === path
            ? 'border-[#dedee0] bg-white font-medium text-[#28292e] dark:border-border dark:bg-[#1c1c1c] dark:text-foreground'
            : 'hover:text-[#28292e] dark:hover:text-foreground'
        "
        :title="path"
        data-testid="artifact-tab"
        :data-active="!directoryOpen && activePath === path"
        @click="emit('activate', path)"
      >
        <FileIcon :is-dir="false" :file-name="nameOf(path)" :size="14" />
        <span class="min-w-0 flex-1 truncate text-left">{{ nameOf(path) }}</span>
        <!-- The close affordance is revealed by hovering the tab (or by the tab
             being active); a permanently visible × on every tab turns the strip
             into a row of buttons. -->
        <span
          role="button"
          tabindex="0"
          class="grid size-4 shrink-0 place-items-center rounded opacity-0 transition-opacity hover:!opacity-100 hover:bg-[#ececec] group-hover/tab:opacity-55 dark:hover:bg-[#2a2a2a]"
          :class="!directoryOpen && activePath === path ? 'opacity-55' : ''"
          :aria-label="`${t('editor.closeTab')}: ${nameOf(path)}`"
          data-testid="artifact-tab-close"
          @click.stop="emit('close', path)"
          @keydown.enter.stop="emit('close', path)"
          @keydown.space.prevent.stop="emit('close', path)"
        >
          <X :size="13" />
        </span>
      </button>

      <button
        type="button"
        class="mt-auto grid h-[38px] w-[30px] shrink-0 place-items-center rounded-t-lg text-[#858b9b] transition-colors hover:bg-[#ececec] hover:text-foreground dark:hover:bg-[#242424]"
        :aria-label="t('workspace.openDirectory')"
        data-testid="artifact-add-tab"
        @click="emit('directory')"
      >
        <Plus :size="15" />
      </button>
    </div>

    <div
      class="flex shrink-0 items-center gap-1 self-stretch border-l border-[#e6e6e6] px-2 dark:border-border"
      :class="directoryOpen ? 'bg-card dark:bg-[#151515]' : 'bg-[#f6f6f6] dark:bg-[#151515]'"
    >
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-[#191919] transition-colors hover:bg-[#efeff1] dark:text-foreground dark:hover:bg-[#242424]"
        :class="directoryOpen ? 'bg-[#e9e9e9] dark:bg-[#2a2a2a]' : ''"
        :aria-label="t('workspace.openDirectory')"
        :aria-pressed="directoryOpen"
        data-testid="artifact-directory-toggle"
        @click="emit('directory')"
      >
        <FolderOpen :size="18" />
      </button>
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-[#191919] transition-colors hover:bg-[#efeff1] dark:text-foreground dark:hover:bg-[#242424]"
        :aria-label="
          fullscreen ? t('workspace.exitFullscreenPreview') : t('workspace.fullscreenPreview')
        "
        data-testid="artifact-fullscreen-toggle"
        @click="emit('toggle-fullscreen')"
      >
        <Minimize2 v-if="fullscreen" :size="17" />
        <Maximize2 v-else :size="17" />
      </button>
      <button
        v-if="canCollapse"
        type="button"
        class="grid size-8 place-items-center rounded-lg text-[#191919] transition-colors hover:bg-[#efeff1] dark:text-foreground dark:hover:bg-[#242424]"
        :aria-label="t('workspace.collapsePanel')"
        data-testid="workspace-collapse"
        @click="emit('collapse')"
      >
        <PanelRight :size="18" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.artifact-tab-scroll {
  scrollbar-width: none;
}
.artifact-tab-scroll::-webkit-scrollbar {
  display: none;
}
</style>
