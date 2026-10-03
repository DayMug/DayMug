<script setup lang="ts">
// Help-document editor. Unlike the read-only config mirrors on this page the
// markdown here is writable and persisted in the database, so admins can
// update onboarding copy without editing config.yaml or restarting. Clearing
// the textarea and saving hides the sidebar's "?" button for every user.
//
// Self-contained: it loads its own value on mount and pushes saves into the
// shared useHelpDoc cache, so no help state lives in the parent.
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminSaveHelpDoc, fetchHelpDoc } from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { useHelpDoc } from "@/composables/useHelpDoc";
import { Button } from "@/components/ui/button";

const { t } = useI18n();
const { setHelpDocMarkdown } = useHelpDoc();

const helpDoc = ref<string>("");
const helpDocSaved = ref<string>(""); // last-known persisted value, for dirty detection
const {
  busy: helpDocSaving,
  error: helpDocErr,
  message: helpDocMsg,
  run: runSaveHelpDoc,
} = useAsyncOperation();
const helpDocDirty = computed(() => helpDoc.value !== helpDocSaved.value);

onMounted(async () => {
  try {
    const doc = await fetchHelpDoc();
    helpDoc.value = doc.markdown;
    helpDocSaved.value = doc.markdown;
  } catch {
    // Non-fatal: the editor still works, it just starts blank.
  }
});

async function onSaveHelpDoc() {
  await runSaveHelpDoc(async () => {
    const res = await adminSaveHelpDoc(helpDoc.value);
    helpDocSaved.value = res.markdown;
    // Push the new value into the shared module-level cache so the sidebar's
    // "?" button appears (or disappears) without a reload — useHelpDoc()
    // returns the same ref to every consumer.
    setHelpDocMarkdown(res.markdown);
    helpDocMsg.value = res.markdown
      ? t("settings.adminGlobal.helpSaved")
      : t("settings.adminGlobal.helpCleared");
  });
}
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4" data-testid="help-document">
    <h2 class="text-[14px] font-semibold mb-2">
      {{ t("settings.adminGlobal.helpDocument") }}
    </h2>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.helpIntro") }}
    </p>
    <textarea
      v-model="helpDoc"
      rows="10"
      spellcheck="false"
      :placeholder="t('settings.adminGlobal.helpPlaceholder')"
      class="w-full font-mono text-[12px] leading-relaxed border border-border rounded-md bg-background p-2 focus:outline-none focus:ring-1 focus:ring-ring resize-y"
    />
    <div class="mt-2 flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        :disabled="helpDocSaving || !helpDocDirty"
        @click="onSaveHelpDoc"
      >
        {{ helpDocSaving ? t("common.saving") : t("common.save") }}
      </Button>
      <span v-if="helpDocDirty && !helpDocSaving" class="text-[11px] text-muted-foreground">
        {{ t("settings.adminGlobal.unsavedChanges") }}
      </span>
    </div>
    <p v-if="helpDocMsg" class="mt-2 text-[12px] text-muted-foreground">{{ helpDocMsg }}</p>
    <p v-if="helpDocErr" class="mt-2 text-[12px] text-red-600">{{ helpDocErr }}</p>
  </section>
</template>
