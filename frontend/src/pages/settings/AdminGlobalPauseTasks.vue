<script setup lang="ts">
// Operator hold on new work, for staging a release without racing live
// prompts. Self-contained like the database panel: it owns both the initial
// read and the toggle round-trip, so the parent only has to mount it.
//
// The switch is driven from server state rather than from the checkbox event:
// the local value only moves once the PUT comes back, so a failed request
// leaves the UI showing what the server is actually doing instead of a state
// the operator merely asked for.
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminFetchPause, adminSetPause } from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { Switch } from "@/components/ui/switch";

const { t } = useI18n();

const paused = ref(false);
// null until the first fetch resolves, which is what keeps the switch
// disabled (and silent) rather than briefly rendering a confident "running".
const loaded = ref(false);
const { busy: saving, error: saveErr, run: runSave } = useAsyncOperation();
const resumedCount = ref<number | null>(null);

onMounted(async () => {
  try {
    paused.value = (await adminFetchPause()).paused;
    loaded.value = true;
  } catch {
    // Leave the switch disabled: a panel that can't read the state must not
    // offer to write it.
  }
});

async function onToggle(next: boolean) {
  resumedCount.value = null;
  const res = await runSave(() => adminSetPause(next));
  if (!res) return;
  paused.value = res.paused;
  // Only meaningful on a resume, and only worth showing when work actually
  // restarted — "resumed 0 conversations" is noise.
  resumedCount.value = !res.paused && res.resumed_conversations ? res.resumed_conversations : null;
}
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4" data-testid="pause-new-tasks">
    <h2 class="text-[14px] font-semibold mb-2">
      {{ t("settings.adminGlobal.pauseTasks") }}
    </h2>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.pauseTasksIntro") }}
    </p>

    <div class="flex items-center justify-between rounded-md border border-border px-3 py-2">
      <span class="text-[13px]">
        {{
          paused ? t("settings.adminGlobal.pauseTasksOn") : t("settings.adminGlobal.pauseTasksOff")
        }}
      </span>
      <Switch
        :model-value="paused"
        :disabled="!loaded || saving"
        data-testid="pause-new-tasks-switch"
        @update:model-value="onToggle"
      />
    </div>

    <p v-if="paused" class="mt-3 text-[12px] text-muted-foreground">
      {{ t("settings.adminGlobal.pauseTasksQueuedNote") }}
    </p>
    <p v-if="resumedCount" class="mt-3 text-[12px] text-muted-foreground">
      {{ t("settings.adminGlobal.pauseTasksResumed", { count: resumedCount }) }}
    </p>
    <p v-if="saveErr" class="mt-3 text-[12px] text-red-600">{{ saveErr }}</p>
  </section>
</template>
