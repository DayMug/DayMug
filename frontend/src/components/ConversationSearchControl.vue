<script setup lang="ts">
import { nextTick, ref } from "vue";
import { Loader2, Search, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";

import Button from "@/components/ui/button/Button.vue";

defineProps<{
  modelValue: string;
  loading?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
}>();

const { t } = useI18n();
const open = ref(false);
const inputEl = ref<HTMLInputElement | null>(null);

async function openSearch() {
  open.value = true;
  await nextTick();
  inputEl.value?.focus();
}

function closeSearch() {
  emit("update:modelValue", "");
  open.value = false;
}
</script>

<template>
  <div>
    <Button
      v-if="!open"
      variant="outline"
      size="sm"
      class="h-9 w-full justify-start gap-2 rounded-[9px] border-[#e5e5e5] bg-white px-3 text-[14px] font-normal text-[#a0a0a0] shadow-none hover:bg-white hover:text-[#777] dark:border-[#303030] dark:bg-[#1f1f1f] dark:hover:bg-[#242424] dark:hover:text-[#bbb]"
      data-testid="conversation-search-toggle"
      aria-expanded="false"
      @click="openSearch"
    >
      <Loader2 v-if="loading" class="size-3.5 animate-spin" aria-hidden="true" />
      <Search v-else class="size-3.5" aria-hidden="true" />
      <span>{{ t("conversations.search") }}</span>
    </Button>

    <div v-else class="relative">
      <Search
        class="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
        aria-hidden="true"
      />
      <input
        ref="inputEl"
        :value="modelValue"
        type="search"
        class="h-9 w-full rounded-[9px] border border-[#e5e5e5] bg-white pl-8 text-[14px] text-foreground outline-none placeholder:text-muted-foreground focus:border-[#c9c9c9] focus:ring-1 focus:ring-[#d8d8d8] dark:border-[#303030] dark:bg-[#1f1f1f]"
        :class="loading ? 'pr-14' : 'pr-8'"
        data-testid="conversation-search-input"
        :aria-label="t('conversations.search')"
        :placeholder="t('conversations.searchPlaceholder')"
        @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
        @keydown.escape.prevent="closeSearch"
      />
      <Loader2
        v-if="loading"
        class="pointer-events-none absolute right-8 top-1/2 size-3.5 -translate-y-1/2 animate-spin text-muted-foreground"
        aria-hidden="true"
      />
      <button
        type="button"
        class="absolute right-1.5 top-1/2 flex size-6 -translate-y-1/2 items-center justify-center rounded text-muted-foreground hover:text-foreground"
        :title="t('conversations.closeSearch')"
        :aria-label="t('conversations.closeSearch')"
        @click="closeSearch"
      >
        <X class="size-3.5" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>
