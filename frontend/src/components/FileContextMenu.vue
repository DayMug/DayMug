<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, nextTick, watch } from "vue";
import type { FileEntry } from "@/composables/useFileApi";
import { isSupportedArchive } from "@/composables/useFileApi";
import { clampMenuPosition } from "@/lib/menuPosition";
import { primaryModifierLabel } from "@/lib/primaryModifier";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const props = defineProps<{
  entry: FileEntry | null;
  entryPath: string;
  x: number;
  y: number;
  hasClipboard: boolean;
  // Current hidden-files toggle, surfaced in the background-variant
  // menu since the workspace top bar no longer carries that button.
  // Optional so per-entry mounts (which never need this) can omit it.
  showHiddenFiles?: boolean;
  // Current view mode for the workspace (grid vs list). Surfaced in
  // the background-variant menu so users can switch from the
  // right-click menu without a top-bar toggle.
  viewMode?: "grid" | "list";
  // Size of the multi-selection backing the right-click. When the menu
  // opens on an unselected entry the panel collapses to that single item,
  // so this is effectively the size of what "Compress to ZIP" will pack
  // (>=1). Used to flip the label between the single/multi variants.
  selectionCount?: number;
  // Optional generic file action used by preview-page file browsing: "Open"
  // follows the panel's default target, while this explicit row keeps a
  // new-tab escape hatch available.
  showOpenInNewTab?: boolean;
}>();

const emit = defineEmits<{
  close: [];
  open: [];
  rename: [];
  // Carry the click event so the panel can read the platform's primary
  // modifier and skip the confirmation prompt for power users who opt out.
  delete: [event: MouseEvent];
  download: [];
  properties: [];
  cut: [];
  copy: [];
  paste: [];
  copyName: [];
  copyPath: [];
  newFile: [];
  newFolder: [];
  upload: [];
  uploadFolder: [];
  refresh: [];
  toggleHiddenFiles: [];
  toggleViewMode: [];
  openInNewTab: [];
  extract: [];
  compressZip: [];
}>();

const isFile = computed(() => props.entry !== null && !props.entry.is_dir);
const showOpenInNewTabItem = computed(() => props.showOpenInNewTab === true && isFile.value);
// "Extract Here" only shows for files whose extension matches an
// open-format archive the backend can actually decompress; everything else
// (.rar, .7z, random binaries) hides the item so we don't surface an action
// that's guaranteed to 400.
const canExtract = computed(
  () => isFile.value && props.entry !== null && isSupportedArchive(props.entry.name),
);
// "Compress to ZIP" label flips between single- and multi-select wording so
// the user knows which set the action is about to pack. The count itself is
// supplied by the panel (it owns the selection set).
const compressLabel = computed(() => {
  const n = props.selectionCount ?? 1;
  return n > 1 ? t("files.menu.compressItemsToZip", { n }) : t("files.menu.compressToZip");
});
const downloadLabel = computed(() => {
  if ((props.selectionCount ?? 1) > 1) return t("files.menu.downloadAsZip");
  return props.entry?.is_dir ? t("files.menu.downloadAsZip") : t("files.menu.download");
});

const modLabel = computed(() => primaryModifierLabel());
const modKey = computed(() => (modLabel.value === "⌘" ? "⌘" : "Ctrl+"));

const isBackground = computed(() => props.entry === null);

// "Copy as" fly-out reveals on hover for pointer devices, but touch devices
// have no hover, so tapping the row would do nothing. Track an explicit
// open state toggled on tap so the submenu is reachable on mobile too.
const copyAsOpen = ref(false);

const menuRef = ref<HTMLElement | null>(null);
const pos = ref({ left: props.x, top: props.y });

// Submenus ("Copy as ▸", "Upload ▸") fly out to the right by default. When
// the menu sits near the viewport's right edge that flyout would spill
// off-screen, so flip it to open leftward instead. Decided once at open time
// from the clamped menu position — the hover-driven panel stays JS-free.
const flipSubmenu = ref(false);
// Approximate flyout width: panel min-width (120px) + a little slack for
// border/padding. Good enough to pick a side; the panel itself never
// exceeds it by much.
const SUBMENU_WIDTH = 140;
const submenuSideClass = computed(() => (flipSubmenu.value ? "right-full mr-0" : "left-full ml-0"));

function handleClickOutside(e: PointerEvent) {
  const el = (e.target as HTMLElement).closest(".file-context-menu");
  if (!el) emit("close");
}

function updateMenuPosition() {
  nextTick(() => {
    if (!menuRef.value) return;
    pos.value = clampMenuPosition(menuRef.value, props.x, props.y);
    const rect = menuRef.value.getBoundingClientRect();
    flipSubmenu.value = pos.value.left + rect.width + SUBMENU_WIDTH > window.innerWidth - 8;
  });
}

watch([() => props.x, () => props.y], updateMenuPosition, { flush: "post" });

onMounted(() => {
  // pointerdown (not mousedown) so the iOS synthetic mousedown that follows
  // a touch long-press doesn't immediately close the menu we just opened —
  // pointerdown fired earlier (at touch start) and won't fire again on lift.
  document.addEventListener("pointerdown", handleClickOutside);
  updateMenuPosition();
});

onUnmounted(() => {
  document.removeEventListener("pointerdown", handleClickOutside);
});
</script>

<template>
  <div
    ref="menuRef"
    class="file-context-menu fixed z-[1000] min-w-[180px] max-w-[calc(100vw-1rem)] bg-popover text-popover-foreground border border-border rounded-md shadow-lg py-0.5 text-[13px]"
    :style="{ left: pos.left + 'px', top: pos.top + 'px' }"
  >
    <template v-if="!isBackground">
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('open')"
      >
        {{ t("files.menu.open") }}
      </button>
      <button
        v-if="showOpenInNewTabItem"
        class="ctx-item ctx-item-open-new-tab flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('openInNewTab')"
      >
        {{ t("files.menu.openInNewTab") }}
      </button>
      <div class="h-px my-0.5 bg-border" />
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('cut')"
      >
        <span class="flex-1">{{ t("files.menu.cut") }}</span>
        <span class="text-muted-foreground text-[11px] ml-6">{{ modKey }}X</span>
      </button>
      <!-- "Copy ▸" submenu: the file/folder clipboard copy and the two
           text variants (name / path) now live under one row, so the menu
           stops growing a new top-level entry per variant. The panel is
           hidden by default and revealed on hover of the wrapping .group
           (no JS state machine on desktop); tap toggles copyAsOpen so it's
           reachable on touch. Inner buttons keep their .ctx-item-copy-file
           / .ctx-item-copy-name / .ctx-item-copy-path selectors so callers
           and tests still target them. -->
      <div class="ctx-submenu group relative">
        <button
          class="ctx-item ctx-item-copy-as flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
          @click="copyAsOpen = !copyAsOpen"
        >
          <span class="flex-1">{{ t("files.menu.copy") }}</span>
          <span class="text-muted-foreground text-[11px] ml-6">›</span>
        </button>
        <div
          class="ctx-submenu-panel absolute top-0 min-w-[120px] bg-popover text-popover-foreground border border-border rounded-md shadow-lg py-0.5 group-hover:block"
          :class="[submenuSideClass, copyAsOpen ? 'block' : 'hidden']"
        >
          <button
            class="ctx-submenu-item ctx-item-copy-file flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
            @click="emit('copy')"
          >
            <span class="flex-1">{{
              entry!.is_dir ? t("files.menu.folder") : t("files.menu.file")
            }}</span>
            <span class="text-muted-foreground text-[11px] ml-6">{{ modKey }}C</span>
          </button>
          <button
            class="ctx-submenu-item ctx-item-copy-name flex items-center w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
            @click="emit('copyName')"
          >
            {{ t("files.menu.name") }}
          </button>
          <button
            class="ctx-submenu-item ctx-item-copy-path flex items-center w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
            @click="emit('copyPath')"
          >
            {{ t("files.menu.path") }}
          </button>
        </div>
      </div>
      <div class="h-px my-0.5 bg-border" />
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('rename')"
      >
        {{ t("files.menu.rename") }}
      </button>
      <button
        class="ctx-item ctx-item-delete flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-destructive text-[13px] text-left cursor-pointer hover:bg-destructive/10 font-[inherit]"
        :title="t('files.menu.deleteShiftHint', { modifier: modLabel })"
        @click="emit('delete', $event)"
      >
        {{ t("files.menu.delete") }}
      </button>
      <div class="h-px my-0.5 bg-border" />
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('download')"
      >
        {{ downloadLabel }}
      </button>
      <button
        v-if="canExtract"
        class="ctx-item ctx-item-extract flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('extract')"
      >
        {{ t("files.menu.extractHere") }}
      </button>
      <button
        class="ctx-item ctx-item-compress flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('compressZip')"
      >
        {{ compressLabel }}
      </button>
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('properties')"
      >
        {{ t("files.menu.properties") }}
      </button>
    </template>
    <template v-else>
      <button
        class="ctx-item ctx-item-new-file flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('newFile')"
      >
        {{ t("files.menu.newFile") }}
      </button>
      <button
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('newFolder')"
      >
        {{ t("files.menu.newFolder") }}
      </button>
      <!-- Flat "Upload files" / "Upload folder" entries. A native file dialog
           can't offer both files and a folder in one picker, so the two modes
           stay as two rows — but flattened out of the old hover submenu, which
           was fiddly to reach. Picking a folder uploads the folder; picking
           files uploads the files. Selectors kept so callers/tests still match. -->
      <button
        class="ctx-item ctx-item-upload-files flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('upload')"
      >
        {{ t("files.menu.uploadFiles") }}
      </button>
      <button
        class="ctx-item ctx-item-upload-folder flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('uploadFolder')"
      >
        {{ t("files.menu.uploadFolder") }}
      </button>
      <button
        v-if="hasClipboard"
        class="ctx-item flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('paste')"
      >
        <span class="flex-1">{{ t("files.menu.paste") }}</span>
        <span class="text-muted-foreground text-[11px] ml-6">{{ modKey }}V</span>
      </button>
      <div class="h-px my-0.5 bg-border" />
      <button
        class="ctx-item ctx-item-toggle-view flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('toggleViewMode')"
      >
        {{ viewMode === "list" ? t("files.menu.viewAsGrid") : t("files.menu.viewAsList") }}
      </button>
      <button
        class="ctx-item ctx-item-toggle-hidden flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('toggleHiddenFiles')"
      >
        {{ showHiddenFiles ? t("files.menu.hideHiddenFiles") : t("files.menu.showHiddenFiles") }}
      </button>
      <button
        class="ctx-item ctx-item-refresh flex items-center justify-between w-full px-3.5 py-1 bg-transparent text-popover-foreground text-[13px] text-left cursor-pointer hover:bg-accent hover:text-accent-foreground font-[inherit]"
        @click="emit('refresh')"
      >
        {{ t("files.menu.refresh") }}
      </button>
    </template>
  </div>
</template>
