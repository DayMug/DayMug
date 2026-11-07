<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  ArrowLeft,
  ChevronRight,
  Download,
  Eye,
  EyeOff,
  Laptop,
  PanelRight,
  RotateCcw,
} from "lucide-vue-next";
import FileIcon from "@/components/FileIcon.vue";
import {
  deviceFileDownloadUrl,
  listDeviceFiles,
  readDeviceFile,
  type ConversationDeviceBinding,
  type DeviceFileEntry,
} from "@/composables/apiDevices";
import { dirLabel } from "@/lib/desktopClient";
import { errorMessage } from "@/lib/errorMessage";
import { formatBytes } from "@/lib/uploadHelpers";

// The file panel of a conversation bound to a folder on a computer: the same
// place the agent's tools work in, read through DayMug-Client. Read-only —
// editing happens in the user's own apps, or by asking the agent.
const props = defineProps<{
  conversationId: string;
  binding: ConversationDeviceBinding;
  canCollapse?: boolean;
}>();
const emit = defineEmits<{ collapse: [] }>();

const { t } = useI18n();

// Text previews beyond this are cut; the full file stays one download away.
const TEXT_PREVIEW_CHARS = 512 * 1024;

const path = ref(".");
const entries = ref<DeviceFileEntry[]>([]);
const truncated = ref(false);
const loading = ref(false);
const error = ref("");
const showHidden = ref(false);

interface Preview {
  path: string;
  name: string;
  kind: "image" | "text" | "other";
  text?: string;
  url?: string;
  clipped?: boolean;
  error?: string;
  loading: boolean;
}
const preview = ref<Preview | null>(null);

const visible = computed(() =>
  entries.value
    .filter((e) => showHidden.value || !e.name.startsWith("."))
    .slice()
    .sort((a, b) => (a.is_dir !== b.is_dir ? (a.is_dir ? -1 : 1) : a.name.localeCompare(b.name))),
);

const rootLabel = computed(() => dirLabel(props.binding.work_dir));
const crumbs = computed(() => {
  const parts = path.value === "." ? [] : path.value.split("/");
  return parts.map((label, i) => ({ label, path: parts.slice(0, i + 1).join("/") }));
});

function join(dir: string, name: string) {
  return dir === "." ? name : `${dir}/${name}`;
}

let listRun = 0;
let listAbort: AbortController | null = null;

async function load(target: string) {
  const run = ++listRun;
  listAbort?.abort();
  const ctrl = new AbortController();
  listAbort = ctrl;
  if (entries.value.length === 0) loading.value = true;
  error.value = "";
  try {
    const res = await listDeviceFiles(props.conversationId, target, ctrl.signal);
    if (run !== listRun) return;
    path.value = res.path || ".";
    entries.value = res.entries;
    truncated.value = res.truncated;
  } catch (e) {
    if (run !== listRun) return;
    error.value = errorMessage(e, t("deviceFiles.loadFailed"));
    if (target !== "." && target !== path.value) return;
    entries.value = [];
  } finally {
    if (run === listRun) loading.value = false;
  }
}

function revokePreview() {
  if (preview.value?.url) URL.revokeObjectURL(preview.value.url);
}

function closePreview() {
  previewAbort?.abort();
  revokePreview();
  preview.value = null;
}

let previewAbort: AbortController | null = null;

async function openFile(entry: DeviceFileEntry) {
  closePreview();
  const filePath = join(path.value, entry.name);
  const ctrl = new AbortController();
  previewAbort = ctrl;
  preview.value = { path: filePath, name: entry.name, kind: "other", loading: true };
  try {
    const res = await readDeviceFile(props.conversationId, filePath, ctrl.signal);
    const type = res.headers.get("Content-Type") ?? "";
    const blob = await res.blob();
    if (ctrl.signal.aborted) return;
    if (type.startsWith("image/")) {
      preview.value = {
        ...preview.value,
        kind: "image",
        url: URL.createObjectURL(blob),
        loading: false,
      };
    } else if (type.startsWith("text/")) {
      const text = await blob.text();
      if (ctrl.signal.aborted) return;
      preview.value = {
        ...preview.value,
        kind: "text",
        text: text.slice(0, TEXT_PREVIEW_CHARS),
        clipped: text.length > TEXT_PREVIEW_CHARS,
        loading: false,
      };
    } else {
      preview.value = { ...preview.value, kind: "other", loading: false };
    }
  } catch (e) {
    if (ctrl.signal.aborted) return;
    preview.value = {
      ...preview.value!,
      loading: false,
      error: errorMessage(e, t("deviceFiles.readFailed")),
    };
  }
}

function open(entry: DeviceFileEntry) {
  if (entry.is_dir) void load(join(path.value, entry.name));
  else void openFile(entry);
}

function navigate(target: string) {
  closePreview();
  void load(target);
}

function refresh() {
  if (preview.value) {
    const name = preview.value.name;
    const entry = entries.value.find((e) => join(path.value, e.name) === preview.value?.path);
    if (entry) void openFile(entry);
    else if (name) closePreview();
  }
  return load(path.value);
}

// A new conversation, or a different folder, starts over at its root.
watch(
  () => [props.conversationId, props.binding.device_id, props.binding.work_dir],
  () => {
    closePreview();
    entries.value = [];
    path.value = ".";
    void load(".");
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  listAbort?.abort();
  closePreview();
});

defineExpose({ refresh });

const iconBtn =
  "grid size-8 place-items-center rounded-lg text-foreground transition-colors hover:bg-[#efeff1] dark:hover:bg-[#242424]";
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-background" data-testid="device-workspace-panel">
    <div
      class="flex h-11 shrink-0 items-center gap-[3px] border-b border-[#eeeeee] bg-card px-2 dark:border-border"
    >
      <span
        class="flex min-w-0 items-center gap-1.5 px-1.5 text-[12px] text-muted-foreground"
        :title="t('deviceFiles.title', { name: binding.device_name, dir: binding.work_dir })"
        data-testid="device-workspace-location"
      >
        <span class="relative shrink-0">
          <Laptop :size="15" />
          <span
            class="absolute -right-0.5 -bottom-0.5 size-1.5 rounded-full"
            :class="binding.online ? 'bg-emerald-500' : 'bg-muted-foreground'"
          />
        </span>
        <span class="truncate">{{ binding.device_name }}</span>
      </span>
      <span class="min-w-2 flex-1" />
      <button
        type="button"
        :class="iconBtn"
        :aria-label="showHidden ? t('deviceFiles.hideHidden') : t('deviceFiles.showHidden')"
        :aria-pressed="showHidden"
        data-testid="device-workspace-hidden"
        @click="showHidden = !showHidden"
      >
        <component :is="showHidden ? Eye : EyeOff" :size="17" />
      </button>
      <button
        type="button"
        :class="iconBtn"
        :aria-label="t('files.menu.refresh')"
        data-testid="device-workspace-refresh"
        @click="refresh"
      >
        <RotateCcw :size="17" />
      </button>
      <button
        v-if="canCollapse"
        type="button"
        :class="iconBtn"
        :aria-label="t('workspace.collapsePanel')"
        data-testid="device-workspace-collapse"
        @click="emit('collapse')"
      >
        <PanelRight :size="18" />
      </button>
    </div>

    <nav
      class="flex shrink-0 items-center gap-0.5 overflow-x-auto bg-[#f7f7f7] px-3 pb-2 pt-3 text-[13px] dark:bg-[#111]"
      data-testid="device-workspace-breadcrumbs"
    >
      <button
        type="button"
        class="shrink-0 rounded px-1 font-semibold text-[#2c2c2c] hover:bg-[#ececec] dark:text-foreground dark:hover:bg-[#242424]"
        :title="binding.work_dir"
        @click="navigate('.')"
      >
        {{ rootLabel }}
      </button>
      <template v-for="c in crumbs" :key="c.path">
        <ChevronRight :size="13" class="shrink-0 text-muted-foreground" />
        <button
          type="button"
          class="shrink-0 rounded px-1 text-muted-foreground hover:bg-[#ececec] hover:text-foreground dark:hover:bg-[#242424]"
          @click="navigate(c.path)"
        >
          {{ c.label }}
        </button>
      </template>
    </nav>

    <!-- File preview -->
    <div v-if="preview" class="flex min-h-0 flex-1 flex-col" data-testid="device-workspace-preview">
      <div
        class="flex h-9 shrink-0 items-center gap-1 border-b border-[#eeeeee] px-2 dark:border-border"
      >
        <button
          type="button"
          class="grid size-7 place-items-center rounded-md hover:bg-accent"
          :aria-label="t('deviceFiles.back')"
          data-testid="device-workspace-back"
          @click="closePreview"
        >
          <ArrowLeft :size="15" />
        </button>
        <span class="min-w-0 flex-1 truncate text-[13px] font-medium" :title="preview.path">{{
          preview.name
        }}</span>
        <a
          class="grid size-7 place-items-center rounded-md hover:bg-accent"
          :href="deviceFileDownloadUrl(conversationId, preview.path)"
          :download="preview.name"
          :aria-label="t('deviceFiles.download')"
          data-testid="device-workspace-download"
        >
          <Download :size="15" />
        </a>
      </div>
      <div class="min-h-0 flex-1 overflow-auto">
        <p v-if="preview.loading" class="p-4 text-sm text-muted-foreground">
          {{ t("common.loading") }}
        </p>
        <p v-else-if="preview.error" class="p-4 text-sm text-destructive">{{ preview.error }}</p>
        <img
          v-else-if="preview.kind === 'image'"
          :src="preview.url"
          :alt="preview.name"
          class="mx-auto max-w-full p-4"
        />
        <template v-else-if="preview.kind === 'text'">
          <pre
            class="whitespace-pre-wrap break-words p-4 font-mono text-[12px] leading-5"
            data-testid="device-workspace-text"
            >{{ preview.text }}</pre
          >
          <p v-if="preview.clipped" class="px-4 pb-4 text-xs text-muted-foreground">
            {{ t("deviceFiles.clipped") }}
          </p>
        </template>
        <p v-else class="p-4 text-sm text-muted-foreground">{{ t("deviceFiles.noPreview") }}</p>
      </div>
    </div>

    <!-- Listing -->
    <div v-else class="min-h-0 flex-1 overflow-auto px-2 pb-3" data-testid="device-workspace-list">
      <p
        v-if="error"
        class="px-2 py-2 text-sm text-destructive"
        data-testid="device-workspace-error"
      >
        {{ error }}
      </p>
      <p v-if="loading" class="px-2 py-6 text-center text-sm text-muted-foreground">
        {{ t("common.loading") }}
      </p>
      <p
        v-else-if="!error && visible.length === 0"
        class="px-2 py-6 text-center text-sm text-muted-foreground"
      >
        {{ t("deviceFiles.empty") }}
      </p>
      <button
        v-for="entry in visible"
        :key="entry.name"
        type="button"
        class="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-[13px] hover:bg-accent"
        :title="entry.name"
        data-testid="device-workspace-entry"
        @click="open(entry)"
      >
        <FileIcon
          :is-dir="entry.is_dir"
          :file-name="entry.name"
          :size="16"
          class="shrink-0 text-muted-foreground"
        />
        <span class="min-w-0 flex-1 truncate">{{ entry.name }}</span>
        <span v-if="!entry.is_dir" class="shrink-0 text-[11px] text-muted-foreground">{{
          formatBytes(entry.size)
        }}</span>
      </button>
      <p v-if="truncated" class="px-2 pt-2 text-xs text-muted-foreground">
        {{ t("deviceFiles.truncated") }}
      </p>
    </div>
  </div>
</template>
