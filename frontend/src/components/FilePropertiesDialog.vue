<script setup lang="ts">
import { X } from "lucide-vue-next";
import type { FileEntry } from "@/composables/useFileApi";
import FileIcon from "./FileIcon.vue";
import FileName from "./FileName.vue";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/format";
import { useTimeFormat } from "@/composables/useTimeFormat";
import { useI18n } from "vue-i18n";

const { timeHour12 } = useTimeFormat();
const { t } = useI18n();

const props = defineProps<{
  entry: FileEntry;
  entryPath: string;
}>();

const emit = defineEmits<{
  close: [];
}>();

function formatSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const size = bytes / Math.pow(1024, i);
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatDate(dateStr: string): string {
  if (!dateStr) return "--";
  const out = formatDateTime(dateStr, timeHour12.value);
  return out || "--";
}

function getExtension(): string {
  if (props.entry.is_dir) return "";
  const parts = props.entry.name.split(".");
  return parts.length > 1 ? "." + parts.pop() : "";
}
</script>

<template>
  <div
    class="props-overlay fixed inset-0 bg-black/30 z-[1001] flex items-center justify-center p-4"
    @click.self="emit('close')"
  >
    <div
      class="bg-card border border-border rounded-lg shadow-2xl w-full max-w-[420px] max-h-[calc(100vh-2rem)] overflow-y-auto text-[13px]"
    >
      <div class="flex items-center justify-between px-4 py-3 border-b border-border">
        <div class="flex items-center gap-2 min-w-0">
          <FileIcon :is-dir="entry.is_dir" :file-name="entry.name" class="text-muted-foreground" />
          <FileName
            :name="entry.name"
            class="props-name font-semibold text-foreground"
            :title="entry.name"
          />
        </div>
        <button
          class="props-close bg-transparent border-none text-muted-foreground cursor-pointer p-1 leading-none hover:text-foreground"
          @click="emit('close')"
        >
          <X class="size-4" />
        </button>
      </div>
      <div class="px-4 py-3">
        <div class="flex py-1.5 gap-3">
          <span class="w-[70px] shrink-0 text-muted-foreground text-xs">{{
            t("files.properties.type")
          }}</span>
          <span class="props-value text-foreground break-all">{{
            entry.is_dir
              ? t("files.properties.folder")
              : t("files.properties.file") + (getExtension() ? " (" + getExtension() + ")" : "")
          }}</span>
        </div>
        <div class="flex py-1.5 gap-3">
          <span class="w-[70px] shrink-0 text-muted-foreground text-xs">{{
            t("files.properties.size")
          }}</span>
          <span class="props-value text-foreground break-all">{{
            entry.is_dir ? "--" : formatSize(entry.size)
          }}</span>
        </div>
        <div class="flex py-1.5 gap-3">
          <span class="w-[70px] shrink-0 text-muted-foreground text-xs">{{
            t("files.properties.modified")
          }}</span>
          <span class="props-value text-foreground break-all">{{
            formatDate(entry.modified)
          }}</span>
        </div>
        <div class="flex py-1.5 gap-3">
          <span class="w-[70px] shrink-0 text-muted-foreground text-xs">{{
            t("files.properties.path")
          }}</span>
          <span class="props-value text-foreground break-all font-mono text-xs">{{
            entryPath
          }}</span>
        </div>
      </div>
      <div class="px-4 pb-3 flex justify-end">
        <Button variant="outline" size="sm" class="props-btn" @click="emit('close')">{{
          t("common.close")
        }}</Button>
      </div>
    </div>
  </div>
</template>
