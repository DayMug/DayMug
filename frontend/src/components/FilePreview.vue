<script setup lang="ts">
import { defineAsyncComponent, nextTick, ref, watch } from "vue";
import { isHtmlFile, shouldInspectTextFile, useFileApi } from "@/composables/useFileApi";
import { apiFetch } from "@/composables/apiClient";
import { renderMarkdown } from "@/composables/useMarkdown";
import { officeAppFor } from "@/lib/office/files";
import { useI18n } from "vue-i18n";
import { errorMessage } from "@/lib/errorMessage";

// Office files carry their own editability (an .xlsm or a legacy .xls opens
// view-only, everything else opens editable), so there is no edit-mode prop:
// the workbench's request would have nothing left to switch.
const props = withDefaults(
  defineProps<{
    userId: string;
    path: string;
    // HTML has two read-only faces: the page it renders to, and its source.
    // Which one is on screen is a host decision (the workbench puts the
    // toggle in its header), so it arrives as a prop rather than living here.
    webView?: boolean;
  }>(),
  { webView: true },
);

const { inspectFile, readFile, downloadFileUrl, readFileUrl } = useFileApi();
const { t } = useI18n();
const OfficePane = defineAsyncComponent(() =>
  import("./OfficePane.vue").then((mod) => mod.default),
);
const HtmlPreview = defineAsyncComponent(() =>
  import("./HtmlPreview.vue").then((mod) => mod.default),
);

const textContent = ref("");
const loading = ref(false);
const error = ref("");

const ext = ref("");
type PreviewType =
  | "text"
  | "markdown"
  | "code"
  | "image"
  | "audio"
  | "video"
  | "pdf"
  | "office"
  | "pptx"
  | "web"
  | "unknown";

const previewType = ref<PreviewType>("unknown");

const imageExts = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico"]);
const audioExts = new Set([".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a"]);
const videoExts = new Set([".mp4", ".webm", ".ogv", ".mov", ".avi", ".mkv"]);
const presentationExts = new Set([".pptx"]);
const codeExts = new Set([
  ".js",
  ".ts",
  ".jsx",
  ".tsx",
  ".vue",
  ".go",
  ".py",
  ".rb",
  ".rs",
  ".java",
  ".c",
  ".cpp",
  ".h",
  ".hpp",
  ".css",
  ".scss",
  ".less",
  ".html",
  ".xml",
  ".yaml",
  ".yml",
  ".toml",
  ".ini",
  ".sh",
  ".bash",
  ".zsh",
  ".fish",
  ".sql",
  ".graphql",
  ".proto",
  ".dockerfile",
  ".makefile",
]);
const textExts = new Set([".txt", ".log", ".env", ".gitignore", ".editorconfig"]);

function detectType(filePath: string): PreviewType {
  const e =
    filePath.lastIndexOf(".") >= 0 ? filePath.slice(filePath.lastIndexOf(".")).toLowerCase() : "";
  ext.value = e;
  const name = filePath.split("/").pop()?.toLowerCase() ?? "";
  if (name === "go.mod" || name === "go.sum") {
    ext.value = ".go";
    return "code";
  }

  if (isHtmlFile(filePath) && props.webView) return "web";
  if (imageExts.has(e)) return "image";
  if (audioExts.has(e)) return "audio";
  if (videoExts.has(e)) return "video";
  if (e === ".pdf") return "pdf";
  if (presentationExts.has(e)) return "pptx";
  if (e === ".md" || e === ".markdown") return "markdown";
  if (codeExts.has(e) || name === "makefile" || name === "dockerfile") return "code";
  if (textExts.has(e) || e === ".json") return "text";
  // .docx / .xlsx / .csv / … are handed to the embedded editors.
  if (officeAppFor(filePath)) return "office";
  return "unknown";
}

async function highlightCode(code: string, extension: string): Promise<string> {
  const langMap: Record<string, string> = {
    ".js": "javascript",
    ".ts": "typescript",
    ".jsx": "javascript",
    ".tsx": "typescript",
    ".vue": "xml",
    ".go": "go",
    ".py": "python",
    ".rb": "ruby",
    ".rs": "rust",
    ".java": "java",
    ".c": "c",
    ".cpp": "cpp",
    ".h": "c",
    ".hpp": "cpp",
    ".css": "css",
    ".scss": "scss",
    ".less": "less",
    ".html": "xml",
    ".xml": "xml",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".toml": "ini",
    ".ini": "ini",
    ".sh": "bash",
    ".bash": "bash",
    ".zsh": "bash",
    ".fish": "bash",
    ".sql": "sql",
    ".json": "json",
    ".graphql": "graphql",
    ".dockerfile": "dockerfile",
    ".makefile": "makefile",
    ".proto": "protobuf",
  };
  // Every language above is in the curated set chat fences already load, so
  // this shares that chunk instead of fetching the full ~190-grammar bundle.
  const hljs = (await import("@/lib/highlight")).default;
  const lang = langMap[extension];
  if (lang) {
    try {
      return hljs.highlight(code, { language: lang }).value;
    } catch {
      // fallback
    }
  }
  return hljs.highlightAuto(code).value;
}

const highlightedCode = ref("");

// Generation token for the load pipeline. `loadCurrent` runs several awaits in
// sequence (inspect → read → highlight / pptx render), so flipping
// quickly between files in the workspace panel used to interleave two runs and
// leave the previous file's text or error under the new file's header. Every
// load takes a `run` and re-checks it after each await; the loaders below take
// the path too, so a superseded run can't re-read `props.path` mid-flight.
let loadRun = 0;

async function loadText(run: number, path: string) {
  loading.value = true;
  error.value = "";
  try {
    const res = await readFile(props.userId, path);
    const text = await res.text();
    if (run !== loadRun) return;
    textContent.value = text;
  } catch (e) {
    if (run !== loadRun) return;
    error.value = errorMessage(e);
  } finally {
    if (run === loadRun) loading.value = false;
  }
}

// Cache-bust counter for binary previews — img/audio/video/iframe URLs are
// otherwise identical across refresh()es and the browser would replay the
// cached response.
const reloadKey = ref(0);

function getDownloadUrl(): string {
  const base = downloadFileUrl(props.userId, props.path);
  if (reloadKey.value === 0) return base;
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}_r=${reloadKey.value}`;
}

// Inline URL for things that need to render in-page (iframe/embed). The
// /files/download endpoint sends Content-Disposition: attachment which
// would make the browser download a PDF instead of rendering it.
function getInlineUrl(): string {
  const base = readFileUrl(props.userId, props.path);
  if (reloadKey.value === 0) return base;
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}_r=${reloadKey.value}`;
}

const pptxContainer = ref<HTMLElement | null>(null);

async function loadPptx(run: number) {
  error.value = "";
  loading.value = true;
  try {
    const res = await apiFetch(getInlineUrl());
    if (run !== loadRun) return;
    if (!res.ok) throw new Error(`download: ${res.status}`);
    const data = await res.arrayBuffer();
    const pptx = await import("pptx-preview");
    if (run !== loadRun) return;
    loading.value = false;
    await nextTick();
    const container = pptxContainer.value;
    if (!container || run !== loadRun) return;
    container.replaceChildren();
    const width = Math.min(Math.max(container.clientWidth - 32, 320), 960);
    const previewer = pptx.init(container, {
      width,
      height: Math.round((width * 9) / 16),
      mode: "list",
    });
    await previewer.preview(data);
  } catch (e) {
    if (run !== loadRun) return;
    error.value = errorMessage(e);
  } finally {
    if (run === loadRun) loading.value = false;
  }
}

async function loadCurrent() {
  const run = ++loadRun;
  const path = props.path;
  let type_ = detectType(path);
  previewType.value = type_;
  textContent.value = "";
  highlightedCode.value = "";
  if (pptxContainer.value) pptxContainer.value.replaceChildren();

  if (type_ === "unknown" && shouldInspectTextFile(path)) {
    loading.value = true;
    error.value = "";
    try {
      const info = await inspectFile(props.userId, path);
      if (run !== loadRun) return;
      if (info.is_text) {
        type_ = "text";
        previewType.value = type_;
      }
    } catch (e) {
      if (run !== loadRun) return;
      error.value = errorMessage(e);
      return;
    } finally {
      if (run === loadRun) loading.value = false;
    }
  }

  if (type_ === "text" || type_ === "markdown") {
    await loadText(run, path);
  } else if (type_ === "code") {
    await loadText(run, path);
    if (run !== loadRun) return;
    const highlighted = await highlightCode(textContent.value, ext.value);
    if (run !== loadRun) return;
    highlightedCode.value = highlighted;
  } else if (type_ === "pptx") {
    await loadPptx(run);
  }
}

watch([() => props.path, () => props.webView], loadCurrent, { immediate: true });

function refresh() {
  reloadKey.value++;
  void loadCurrent();
}

// Saving is owned by the embedded editor; expose a pass-through so the page
// header's Save button can drive it. Returns false when the current file isn't
// an editable office document.
const officeRef = ref<{ save: () => Promise<boolean>; isDirty: () => boolean } | null>(null);
async function save(): Promise<boolean> {
  return (await officeRef.value?.save()) ?? false;
}

// Pass through the editor's unsaved-changes flag so the preview window can
// guard against closing the tab with pending edits. Read on demand (not
// reactive) — only the close handler needs it.
function isDirty(): boolean {
  return officeRef.value?.isDirty() ?? false;
}

defineExpose({ refresh, save, isDirty });
</script>

<template>
  <div class="h-full overflow-auto">
    <div v-if="loading" class="py-10 px-4 md:px-5 text-center text-muted-foreground text-sm">
      {{ t("files.preview.loading") }}
    </div>
    <div v-else-if="error" class="py-10 px-4 md:px-5 text-center text-destructive text-sm">
      {{ error }}
    </div>

    <!-- Image -->
    <div v-else-if="previewType === 'image'" class="flex items-center justify-center p-4">
      <img
        :src="getDownloadUrl()"
        :alt="path"
        class="max-w-full max-h-[80vh] object-contain rounded"
      />
    </div>

    <!-- Audio -->
    <div v-else-if="previewType === 'audio'" class="flex items-center justify-center py-10 px-4">
      <audio controls :src="getDownloadUrl()" class="max-w-full"></audio>
    </div>

    <!-- Video -->
    <div v-else-if="previewType === 'video'" class="flex items-center justify-center py-10 px-4">
      <video controls :src="getDownloadUrl()" class="max-w-full"></video>
    </div>

    <!-- PDF -->
    <div v-else-if="previewType === 'pdf'" class="h-full">
      <iframe :src="getInlineUrl()" class="w-full h-full border-none" frameborder="0"></iframe>
    </div>

    <!-- PPTX (rendered by pptx-preview into its own container) -->
    <div v-else-if="previewType === 'pptx'" ref="pptxContainer" class="pptx-host"></div>

    <!-- HTML rendered as a page, with its own assets -->
    <div v-else-if="previewType === 'web'" class="h-full min-h-0">
      <HtmlPreview :user-id="userId" :path="path" :reload-token="reloadKey" />
    </div>

    <!-- Markdown (renderMarkdown sanitizes; see eslint.config vue/no-v-html note).
         A rendered document sits on a sheet over a grey canvas, as in the
         Iris reference, so a report reads as a page rather than more chat. -->
    <div
      v-else-if="previewType === 'markdown'"
      class="markdown-canvas flex min-h-full flex-col bg-[#efeff0] p-2.5 md:p-[18px] dark:bg-background"
    >
      <div
        v-mermaid
        class="preview-text markdown-body doc-paper mx-auto w-full max-w-[690px] flex-1 bg-white px-[18px] py-[22px] shadow-[0_3px_14px_rgba(30,30,34,0.06)] md:p-[clamp(22px,3vw,38px)] dark:bg-card"
        v-html="renderMarkdown(textContent)"
      ></div>
    </div>

    <!-- Code (highlight.js escapes; see eslint.config vue/no-v-html note) -->
    <pre
      v-else-if="previewType === 'code'"
      class="m-0 p-4 text-[13px] leading-normal overflow-auto bg-muted text-foreground"
    ><code class="font-mono" v-html="highlightedCode"></code></pre>

    <!-- Plain text with line numbers -->
    <div
      v-else-if="previewType === 'text'"
      class="flex m-0 text-[13px] leading-normal text-foreground overflow-auto"
    >
      <div
        class="text-right select-none px-2 md:px-3 py-4 text-muted-foreground/60 font-mono shrink-0"
      >
        <div v-for="n in (textContent.match(/\n/g) ?? []).length + 1" :key="n">{{ n }}</div>
      </div>
      <pre class="p-4 pl-0 whitespace-pre-wrap break-words m-0 font-mono flex-1">{{
        textContent
      }}</pre>
    </div>

    <!-- Spreadsheet / document, in an embedded CasualOffice editor -->
    <div v-else-if="previewType === 'office'" class="h-full min-h-0">
      <OfficePane
        :key="`${path}#${reloadKey}`"
        ref="officeRef"
        :user-id="userId"
        :path="path"
        :app="officeAppFor(path) ?? 'sheet'"
      />
    </div>

    <!-- Unknown -->
    <div v-else class="flex flex-col items-center gap-3 py-[60px] md:px-5 text-muted-foreground">
      <div
        class="size-12 rounded-full bg-muted flex items-center justify-center text-2xl font-bold"
      >
        ?
      </div>
      <div class="text-sm">{{ t("files.preview.unavailable") }}</div>
      <a
        :href="getDownloadUrl()"
        class="px-5 py-2 rounded-md bg-primary text-primary-foreground no-underline text-[13px] font-semibold hover:bg-primary/90"
        download
        >{{ t("files.preview.downloadFile") }}</a
      >
    </div>
  </div>
</template>

<style scoped>
.pptx-host {
  min-height: 100%;
  background: var(--muted);
  padding: 1rem;
  overflow: auto;
}

.pptx-host :deep(.slide) {
  margin: 0 auto 1rem;
  box-shadow: 0 1px 3px rgb(0 0 0 / 0.14);
}
</style>
