<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { ArrowLeft, Folder, FolderPlus } from "lucide-vue-next";
import { browseDirs, mkdirBrowseDir } from "@/composables/useApi";
import type { BrowseDirEntry } from "@/composables/useApi";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useI18n } from "vue-i18n";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();

const props = defineProps<{
  modelValue: string;
  // Optional jail root. When set, the picker opens at this path (or
  // somewhere inside it) and the "Up" button stops at the root — matches
  // the backend home-jail in requireWorkDirWithinCaller, so the user
  // can't even attempt to pick a path the server would then reject.
  rootPath?: string;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
}>();

const isOpen = ref(false);
const currentPath = ref("");
const parentPath = ref("");
const dirs = ref<BrowseDirEntry[]>([]);
const loading = ref(false);
const error = ref("");

// True when the listing is at the configured jail root. The "Up" button
// is also disabled by !parentPath, but the backend additionally clears
// `parent` for us once the caller would walk above their home — this
// flag lets us swap the disabled-reason copy in the UI.
const atRoot = computed(() => {
  if (!props.rootPath) return false;
  return normalize(currentPath.value) === normalize(props.rootPath);
});

function normalize(p: string): string {
  // Trim trailing separators so "/home/alice" matches "/home/alice/".
  return p.replace(/\/+$/, "");
}

// Inline "new folder" form state. Hidden by default; revealed by the
// FolderPlus button in the toolbar. We use an inline input rather than a
// modal so the picker stays compact and the new folder lands right where
// the user is currently looking.
const creatingFolder = ref(false);
const newFolderName = ref("");
const newFolderError = ref("");
// Distinct from `creatingFolder` (which controls the inline form's
// visibility): `mkdirBusy` flips true only while the POST is in flight,
// so a fast double-Enter / double-click on Create can't fire two
// mkdirBrowseDir calls.
const mkdirBusy = ref(false);
const newFolderInput = ref<HTMLInputElement | null>(null);

async function loadDir(path?: string) {
  loading.value = true;
  error.value = "";
  try {
    const result = await browseDirs(path);
    currentPath.value = result.current;
    parentPath.value = result.parent;
    dirs.value = result.dirs;
  } catch {
    error.value = t("files.picker.failedToLoad");
    dirs.value = [];
  } finally {
    loading.value = false;
  }
}

// isInsideRoot returns true if `path` is the jail root or a descendant.
// Mirrors the server's isPathWithin so the UI fails the same way.
function isInsideRoot(path: string): boolean {
  if (!props.rootPath) return true;
  const r = normalize(props.rootPath);
  const p = normalize(path);
  if (!r) return true;
  return p === r || p.startsWith(r + "/");
}

function open() {
  isOpen.value = true;
  cancelNewFolder();
  // If the current value sits outside the jail (or is empty), open at
  // the root instead — same default the form relies on so a fresh agent
  // lands somewhere valid without the user having to navigate.
  const start =
    props.modelValue && isInsideRoot(props.modelValue)
      ? props.modelValue
      : props.rootPath || undefined;
  loadDir(start);
}

function close() {
  isOpen.value = false;
  cancelNewFolder();
}

function navigateTo(path: string) {
  loadDir(path);
}

function goUp() {
  if (!parentPath.value) return;
  // Belt-and-suspenders: the backend already clears `parent` once
  // walking up would leave the jail, but if the prop changes mid-flight
  // the cached value could still point above the root. Refuse here too.
  if (!isInsideRoot(parentPath.value)) return;
  loadDir(parentPath.value);
}

function selectCurrent() {
  emit("update:modelValue", currentPath.value);
  close();
}

function startNewFolder() {
  creatingFolder.value = true;
  newFolderName.value = "";
  newFolderError.value = "";
  nextTick(() => {
    newFolderInput.value?.focus();
  });
}

function cancelNewFolder() {
  creatingFolder.value = false;
  newFolderName.value = "";
  newFolderError.value = "";
}

async function commitNewFolder() {
  if (mkdirBusy.value) return;
  const name = newFolderName.value.trim();
  if (!name) {
    cancelNewFolder();
    return;
  }
  newFolderError.value = "";
  mkdirBusy.value = true;
  try {
    const entry = await mkdirBrowseDir(currentPath.value, name);
    cancelNewFolder();
    // Drop into the freshly created folder so a follow-up "Select This" is
    // one click away. The picker user just told us this is where they want
    // to be.
    await loadDir(entry.path);
  } catch (err) {
    newFolderError.value = errorMessage(err);
  } finally {
    mkdirBusy.value = false;
  }
}

watch(
  () => props.modelValue,
  () => {
    if (isOpen.value) {
      loadDir(props.modelValue || undefined);
    }
  },
);
</script>

<template>
  <div class="relative">
    <div class="flex gap-2">
      <Input
        :model-value="modelValue"
        class="form-input flex-1 cursor-pointer"
        :placeholder="t('files.picker.placeholder')"
        readonly
      />
      <Button
        variant="outline"
        type="button"
        class="browse-btn whitespace-nowrap font-semibold"
        @click="open"
        >{{ t("files.picker.browse") }}</Button
      >
    </div>

    <div
      v-if="isOpen"
      class="dir-browser mt-2 border border-border rounded-lg bg-input overflow-hidden"
    >
      <div class="flex items-center gap-2 px-2.5 py-2 border-b border-border bg-muted">
        <Button
          variant="outline"
          size="sm"
          class="h-7 px-2.5 text-xs whitespace-nowrap inline-flex items-center gap-1"
          :disabled="!parentPath || atRoot"
          :title="atRoot ? t('files.picker.alreadyAtHome') : ''"
          @click="goUp"
        >
          <ArrowLeft class="size-3.5" />
          {{ t("files.picker.up") }}
        </Button>
        <span
          class="flex-1 text-xs font-mono text-muted-foreground overflow-hidden text-ellipsis whitespace-nowrap"
          >{{ currentPath }}</span
        >
        <Button
          variant="outline"
          size="sm"
          class="new-folder-btn h-7 px-2.5 text-xs whitespace-nowrap inline-flex items-center gap-1"
          :title="t('files.picker.newFolderTitle')"
          :disabled="loading || !!error"
          @click="startNewFolder"
        >
          <FolderPlus class="size-3.5" />
          {{ t("files.picker.new") }}
        </Button>
        <Button
          size="sm"
          class="select-btn h-7 px-2.5 text-xs whitespace-nowrap"
          @click="selectCurrent"
          >{{ t("files.picker.selectThis") }}</Button
        >
      </div>
      <div
        v-if="creatingFolder"
        class="new-folder-form flex items-center gap-2 px-2.5 py-1.5 border-b border-border bg-card"
      >
        <Folder class="size-4 shrink-0 text-muted-foreground" />
        <input
          ref="newFolderInput"
          v-model="newFolderName"
          class="flex-1 min-w-0 text-[13px] bg-transparent outline-none border-b border-border focus:border-primary"
          :placeholder="t('files.picker.folderName')"
          @keydown.enter.prevent="commitNewFolder"
          @keydown.escape.prevent="cancelNewFolder"
        />
        <Button
          size="sm"
          class="h-7 px-2.5 text-xs whitespace-nowrap"
          :disabled="!newFolderName.trim() || mkdirBusy"
          @click="commitNewFolder"
          >{{ t("common.create") }}</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2.5 text-xs whitespace-nowrap"
          @click="cancelNewFolder"
          >{{ t("common.cancel") }}</Button
        >
      </div>
      <div v-if="newFolderError" class="px-3 py-1 text-[11px] text-destructive bg-destructive/10">
        {{ newFolderError }}
      </div>
      <div v-if="loading" class="p-4 text-center text-[13px] text-muted-foreground">
        {{ t("files.picker.loading") }}
      </div>
      <div v-else-if="error" class="p-4 text-center text-[13px] text-destructive">{{ error }}</div>
      <div v-else class="max-h-60 overflow-y-auto">
        <div
          v-for="d in dirs"
          :key="d.path"
          class="dir-item flex items-center gap-2 px-3 py-1.5 cursor-pointer text-[13px] text-foreground hover:bg-accent hover:text-accent-foreground"
          @click="navigateTo(d.path)"
          @dblclick="
            emit('update:modelValue', d.path);
            close();
          "
        >
          <Folder class="size-4 shrink-0 text-muted-foreground" />
          <span class="overflow-hidden text-ellipsis whitespace-nowrap">{{ d.name }}</span>
        </div>
        <div v-if="dirs.length === 0" class="p-4 text-center text-[13px] text-muted-foreground">
          {{ t("files.picker.noSubdirectories") }}
        </div>
      </div>
      <div class="flex items-center justify-between px-2.5 py-1.5 border-t border-border">
        <span class="text-[11px] text-muted-foreground">{{ t("files.picker.hint") }}</span>
        <Button variant="ghost" size="sm" class="cancel-btn h-7 px-2.5 text-xs" @click="close">{{
          t("common.cancel")
        }}</Button>
      </div>
    </div>
  </div>
</template>
