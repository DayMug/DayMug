<script setup lang="ts">
/**
 * The spreadsheet and document surfaces: a CasualOffice editor in an iframe,
 * wired to the workspace file API by `lib/office/host.ts`.
 *
 * Both editors share this one component because the host's job is identical —
 * mount an iframe, bridge the file API, follow the theme. Splitting it in two
 * would only let the two copies drift. The differences are all routed through
 * the `app` prop:
 *
 * | | sheet | docs |
 * |---|---|---|
 * | Save | parent-driven button; nothing is written implicitly | **the editor autosaves**; the host has no trigger |
 * | Address | fixed URL, file requested via `load.request` | docId in the query (its only config channel) |
 * | Theme | follows light/dark | no `set.theme` in its protocol |
 *
 * ## Reloading means remounting the iframe
 *
 * The host has no "re-read the file" command, so `frame` is bumped to rebuild
 * the iframe; the editor then issues a fresh `casual.load.request` on startup.
 * That is the path taken when an agent rewrites the open file.
 */
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

import { apiFetch } from "@/composables/apiClient";
import { useFileApi } from "@/composables/useFileApi";
import { useConfirm } from "@/composables/useConfirm";
import { useTheme } from "@/composables/useTheme";
import { docsEmbedUrl, embedLocale, sheetsEmbedUrl } from "@/lib/office/embed";
import { isReadOnly, type OfficeApp } from "@/lib/office/files";
import { OfficeHost, type LossyWarning, type OfficeFileGateway } from "@/lib/office/host";
import { errorMessage } from "@/lib/errorMessage";

const props = defineProps<{
  userId: string;
  path: string;
  app: OfficeApp;
}>();

const { t, locale } = useI18n();
const { isDark } = useTheme();
const { confirm } = useConfirm();
const { readFileUrl, uploadFiles } = useFileApi();

const iframe = ref<HTMLIFrameElement | null>(null);
/** Bumping this remounts the iframe — the only way to make the editor reload. */
const frame = ref(0);
const ready = ref(false);
const error = ref("");

let host: OfficeHost | null = null;

const readOnly = computed(() => isReadOnly(props.path));

const src = computed(() => {
  const embed = embedLocale(locale.value);
  return props.app === "docs"
    ? docsEmbedUrl(props.path, embed)
    : sheetsEmbedUrl({ readOnly: readOnly.value, locale: embed });
});

/** Read and write raw bytes. Injected into the host so it stays testable. */
const files: OfficeFileGateway = {
  async read(path) {
    const res = await apiFetch(readFileUrl(props.userId, path));
    if (!res.ok) throw new Error(`read: ${res.status}`);
    return res.arrayBuffer();
  },
  async write(path, bytes, contentType) {
    const slash = path.lastIndexOf("/");
    const dir = slash >= 0 ? path.slice(0, slash) : "";
    const name = path.slice(slash + 1);
    const file = new File([bytes], name, { type: contentType });
    await uploadFiles(props.userId, dir, [file], { onConflict: "overwrite" });
  },
};

/**
 * The lossy-save warning. Says what is lost and points at the way to keep it,
 * without scaring anyone off — a plain-data csv may be exactly what was wanted.
 */
async function confirmLossy(warning: LossyWarning): Promise<boolean> {
  const lines = [
    warning.sheetCount > 1
      ? t("files.office.lossySheets", { dropped: warning.sheetCount - 1 })
      : t("files.office.lossyFormatting"),
    t("files.office.lossyFormulas"),
  ];
  if (warning.reencodesTo) lines.push(t("files.office.lossyEncoding"));
  lines.push(t("files.office.lossyHint"));

  return confirm({
    title: t("files.office.lossyTitle"),
    message: lines.join(" "),
    confirmText: t("files.office.lossyConfirm"),
    cancelText: t("common.cancel"),
  });
}

function attach(): void {
  const win = iframe.value?.contentWindow;
  if (!win) return;
  host = new OfficeHost({
    app: props.app,
    path: props.path,
    files,
    iframeWindow: win,
    origin: window.location.origin,
    confirmLossy,
    onReady: () => {
      ready.value = true;
      error.value = "";
      host?.setTheme(isDark.value ? "dark" : "light");
    },
    onSaved: () => {
      error.value = "";
    },
    onError: (e) => {
      error.value = errorMessage(e);
    },
  });
}

function detach(): void {
  host?.destroy();
  host = null;
  ready.value = false;
}

/** Save on demand, for the header button and Ctrl/Cmd+S. */
async function save(): Promise<boolean> {
  if (!host || !ready.value) return false;
  error.value = "";
  return host.save();
}

/**
 * Whether closing should ask first.
 *
 * There is no save-on-exit for spreadsheets. An unconditional one would
 * rewrite every file the user merely opened, pushing it back out through the
 * editor's writer and dropping whatever that writer does not model; and the
 * embed offers no change signal precise enough to make it conditional (see
 * `OfficeHost.watchInteraction`). So the contract is the one the app had
 * before: Save, or Ctrl/Cmd+S, and a confirmation if you leave without it.
 */
function isDirty(): boolean {
  return host?.isDirty() ?? false;
}

function refresh(): void {
  detach();
  error.value = "";
  frame.value += 1;
}

watch(isDark, (dark) => host?.setTheme(dark ? "dark" : "light"));
// `src` already folds in path, app and UI language; userId only changes which
// workspace the same path resolves against, and that needs a reload too.
watch(() => [props.userId, src.value], refresh);

onBeforeUnmount(detach);

defineExpose({ save, isDirty, refresh });
</script>

<template>
  <div class="office-pane">
    <div v-if="error" class="office-error-banner">{{ error }}</div>
    <div class="relative min-h-0 flex-1">
      <div
        v-if="!ready"
        class="absolute inset-0 z-10 flex items-center justify-center bg-background text-sm text-muted-foreground"
      >
        {{ t("files.spreadsheet.loading") }}
      </div>
      <iframe
        :key="frame"
        ref="iframe"
        :src="src"
        class="size-full border-0"
        :title="app === 'docs' ? t('files.office.documentTitle') : t('files.office.sheetTitle')"
        data-testid="office-frame"
        @load="attach"
      ></iframe>
    </div>
  </div>
</template>

<style scoped>
.office-pane {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
  background: var(--background);
}

.office-error-banner {
  flex-shrink: 0;
  padding: 0.6rem 0.85rem;
  border-bottom: 1px solid var(--border);
  background: color-mix(in srgb, var(--destructive) 12%, var(--background));
  color: var(--destructive);
  font-size: 12px;
  word-break: break-word;
}
</style>
