<script setup lang="ts">
import { useI18n } from "vue-i18n";
import {
  ArrowUp,
  FilePlus2,
  FolderOpen,
  FolderPlus,
  LayoutGrid,
  LayoutList,
  PanelRight,
  RotateCcw,
  Upload,
} from "lucide-vue-next";

defineProps<{
  breadcrumbs: { label: string; path: string }[];
  isDragOverTopBar: boolean;
  canDropToParent: boolean;
  canCollapse?: boolean;
  viewMode: "grid" | "list";
}>();

const emit = defineEmits<{
  navigate: [path: string];
  collapse: [];
  "set-view-mode": ["grid" | "list"];
  "new-folder": [];
  "new-file": [];
  upload: [];
  refresh: [];
}>();

const { t } = useI18n();
</script>

<template>
  <div
    class="ws-topbar shrink-0"
    :class="{
      'is-drop-target bg-primary/10 ring-2 ring-primary ring-inset':
        isDragOverTopBar && canDropToParent,
    }"
    data-testid="workspace-topbar"
  >
    <!-- Action rail. The only bordered bar in the browser: everything below it
         is one continuous canvas, so the current location reads as a heading
         over the files rather than as a second chrome strip. -->
    <div
      class="flex h-11 items-center gap-[3px] border-b border-[#eeeeee] bg-card px-2 dark:border-border"
    >
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]"
        :aria-label="t('files.menu.newFolder')"
        data-testid="workspace-new-folder"
        @click="emit('new-folder')"
      >
        <FolderPlus :size="18" />
      </button>
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]"
        :aria-label="t('files.menu.newFile')"
        data-testid="workspace-new-file"
        @click="emit('new-file')"
      >
        <FilePlus2 :size="18" />
      </button>
      <span class="mx-0.5 h-3.5 w-px bg-[#dddddd] dark:bg-border" aria-hidden="true" />
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]"
        :aria-label="t('files.menu.uploadFiles')"
        data-testid="workspace-upload"
        @click="emit('upload')"
      >
        <Upload :size="18" />
      </button>
      <span class="mx-0.5 h-3.5 w-px bg-[#dddddd] dark:bg-border" aria-hidden="true" />
      <button
        type="button"
        class="grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]"
        :aria-label="t('files.menu.refresh')"
        data-testid="workspace-refresh"
        @click="emit('refresh')"
      >
        <RotateCcw :size="18" />
      </button>

      <span class="min-w-2 flex-1" />
      <div
        class="ws-view-switch flex items-center rounded-lg bg-[#efeff1] p-0.5 dark:bg-[#242424]"
        data-testid="workspace-view-switch"
      >
        <button
          v-for="mode in ['grid', 'list'] as const"
          :key="mode"
          type="button"
          class="grid size-7 place-items-center rounded-md text-muted-foreground transition-colors hover:text-foreground"
          :class="viewMode === mode ? 'bg-white text-foreground dark:bg-[#333]' : ''"
          :aria-pressed="viewMode === mode"
          :aria-label="mode === 'grid' ? t('files.menu.viewAsGrid') : t('files.menu.viewAsList')"
          :data-testid="`workspace-view-${mode}`"
          @click="emit('set-view-mode', mode)"
        >
          <component :is="mode === 'grid' ? LayoutGrid : LayoutList" :size="18" />
        </button>
      </div>
      <button
        v-if="canCollapse"
        type="button"
        class="ml-[3px] grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]"
        :aria-label="t('workspace.collapsePanel')"
        data-testid="workspace-collapse"
        @click="emit('collapse')"
      >
        <PanelRight :size="18" />
      </button>
    </div>

    <!-- Location heading. Sits on the file canvas, not in a bar of its own:
         same background, no rule, and the trailing path is de-emphasised so
         only the root reads as a title. -->
    <div
      class="ws-location flex items-center gap-[7px] bg-[#f7f7f7] px-4 pb-[9px] pt-4 dark:bg-[#111]"
    >
      <div
        v-if="isDragOverTopBar && canDropToParent"
        class="ws-topbar-hint flex min-w-0 flex-1 items-center gap-1.5 text-xs font-medium text-primary"
        data-testid="workspace-topbar-hint"
      >
        <ArrowUp :size="15" />
        <span class="truncate">{{ t("workspace.dropToParentHint") }}</span>
      </div>
      <template v-else>
        <FolderOpen :size="17" class="shrink-0 text-[#2c2c2c] dark:text-foreground" />
        <button
          class="home-btn shrink-0 rounded px-0.5 text-[14px] font-semibold text-[#2c2c2c] transition-colors hover:bg-[#ececec] dark:text-foreground dark:hover:bg-[#242424]"
          :title="t('workspace.home')"
          @click="emit('navigate', '.')"
        >
          {{ t("workspace.home") }}
        </button>
        <div
          class="ws-breadcrumbs flex min-w-0 flex-1 items-center gap-1 overflow-x-auto text-[12px] text-[#999999]"
        >
          <template v-for="(crumb, i) in breadcrumbs" :key="crumb.path">
            <template v-if="i > 0">
              <span class="shrink-0 text-[#c4c4c4]">/</span>
              <button
                v-if="i < breadcrumbs.length - 1"
                class="crumb-btn shrink-0 whitespace-nowrap rounded px-0.5 transition-colors hover:bg-[#ececec] hover:text-foreground dark:hover:bg-[#242424]"
                @click="emit('navigate', crumb.path)"
              >
                {{ crumb.label }}
              </button>
              <span v-else class="crumb-btn current shrink-0 whitespace-nowrap px-0.5">
                {{ crumb.label }}
              </span>
            </template>
          </template>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.ws-breadcrumbs {
  scrollbar-width: none;
}
.ws-breadcrumbs::-webkit-scrollbar {
  display: none;
}
</style>
