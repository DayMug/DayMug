<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Button } from "@/components/ui/button";
import { fetchMyEnvironment, updateMyEnvironment } from "@/composables/useApi";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.advanced.title")));

const env = ref("");
const loading = ref(true);
const { busy: submitting, error: errorMsg, message: successMsg, run } = useAsyncOperation();
const lineNumberGutter = ref<HTMLDivElement>();
const lineNumbers = computed(() => env.value.split("\n").length);

onMounted(() =>
  run(
    async () => {
      env.value = (await fetchMyEnvironment()).env;
    },
    { busy: loading, errorFallback: t("settings.advanced.errors.load") },
  ),
);

async function onSubmit() {
  if (submitting.value) return;
  await run(
    async () => {
      env.value = (await updateMyEnvironment(env.value)).env;
      successMsg.value = t("settings.advanced.success");
    },
    { errorFallback: t("settings.advanced.errors.save") },
  );
}

function syncLineNumbers(event: Event) {
  const textarea = event.currentTarget as HTMLTextAreaElement;
  if (lineNumberGutter.value) {
    lineNumberGutter.value.style.transform = `translateY(-${textarea.scrollTop}px)`;
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[640px] px-4 pb-8">
    <SettingsDetailHeader :title="t('settings.advanced.title')" />

    <form class="max-w-md space-y-4" @submit.prevent="onSubmit">
      <div>
        <h4 class="text-[13px] font-semibold text-foreground">
          {{ t("settings.advanced.environment") }}
        </h4>
        <p class="mt-1 text-[12px] text-muted-foreground">
          {{ t("settings.advanced.environmentHelp") }}
        </p>
      </div>

      <!-- The gutter sits beside the textarea rather than over its left
           padding: rows are unwrapped, and once a long one scrolls the field
           sideways that padding scrolls away with it and the text slides
           under the numbers. A sibling keeps the field's own edge as the clip. -->
      <div
        data-env-editor
        class="flex overflow-hidden rounded-md border border-input bg-background shadow-sm focus-within:ring-1 focus-within:ring-ring has-[:disabled]:opacity-50"
      >
        <!-- data-line-gutter: touch devices floor form-control text at 16px to
             stop iOS zooming in on focus, and the gutter has to take the same
             floor or the numbers stop lining up with the rows. -->
        <div
          aria-hidden="true"
          data-line-gutter
          class="pointer-events-none w-10 shrink-0 overflow-hidden border-r border-input bg-muted/40 text-right font-mono text-[12px] leading-5 text-muted-foreground select-none"
        >
          <div ref="lineNumberGutter" class="py-2 pr-2 will-change-transform">
            <div v-for="lineNumber in lineNumbers" :key="lineNumber" data-line-number>
              {{ lineNumber }}
            </div>
          </div>
        </div>
        <textarea
          id="advanced-environment"
          v-model="env"
          rows="10"
          wrap="off"
          spellcheck="false"
          :disabled="loading || submitting"
          :placeholder="t('settings.advanced.placeholder')"
          class="block min-w-0 flex-1 resize-y overflow-auto whitespace-pre bg-transparent px-3 py-2 font-mono text-[12px] leading-5 outline-none disabled:cursor-not-allowed"
          @scroll="syncLineNumbers"
        ></textarea>
      </div>

      <p class="text-[11px] text-muted-foreground">
        {{ t("settings.advanced.precedence") }}
      </p>

      <div
        v-if="errorMsg"
        role="alert"
        class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {{ errorMsg }}
      </div>
      <div
        v-if="successMsg"
        role="status"
        class="rounded-md border border-green-700/30 bg-green-700/10 px-3 py-2 text-sm text-green-700"
      >
        {{ successMsg }}
      </div>

      <Button type="submit" size="sm" :disabled="loading || submitting">
        {{ submitting ? t("settings.advanced.saving") : t("settings.advanced.submit") }}
      </Button>
    </form>
  </div>
</template>
