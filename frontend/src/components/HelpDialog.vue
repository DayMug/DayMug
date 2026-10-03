<script setup lang="ts">
import { computed } from "vue";
import { X } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import { renderMarkdown } from "@/composables/useMarkdown";
import "@/assets/markdown.css";
import { useI18n } from "vue-i18n";

const props = defineProps<{
  markdown: string;
}>();

const emit = defineEmits<{
  close: [];
}>();

const { t } = useI18n();
const html = computed(() => renderMarkdown(props.markdown || ""));
</script>

<template>
  <div
    class="fixed inset-0 bg-black/30 z-[1001] flex items-center justify-center p-4"
    @click.self="emit('close')"
  >
    <div
      class="bg-card border border-border rounded-lg shadow-2xl w-full max-w-[640px] max-h-[calc(100vh-2rem)] flex flex-col text-[13px]"
    >
      <div class="flex items-center justify-between px-4 py-3 border-b border-border">
        <span class="font-semibold text-foreground">{{ t("help.title") }}</span>
        <button
          class="bg-transparent border-none text-muted-foreground cursor-pointer p-1 leading-none hover:text-foreground"
          :aria-label="t('common.close')"
          @click="emit('close')"
        >
          <X class="size-4" />
        </button>
      </div>
      <div v-mermaid class="px-4 py-3 overflow-y-auto markdown-body" v-html="html" />
      <div class="px-4 pb-3 flex justify-end border-t border-border pt-3">
        <Button variant="outline" size="sm" @click="emit('close')">{{ t("common.close") }}</Button>
      </div>
    </div>
  </div>
</template>
