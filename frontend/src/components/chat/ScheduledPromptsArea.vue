<script setup lang="ts">
// Messages the user chose to send later. They sit apart from the queued
// prompts above the composer because they wait on a clock, not on the running
// task; × takes one back into the editor.
import { Clock, Paperclip, X } from "lucide-vue-next";
import { useNow } from "@vueuse/core";
import { useI18n } from "vue-i18n";
import type { ScheduledPrompt } from "@/composables/useChat";
import { useTimeFormat } from "@/composables/useTimeFormat";
import { formatSendAt, formatUntil } from "@/lib/scheduleTime";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

defineProps<{ prompts: ScheduledPrompt[] }>();

const emit = defineEmits<{ cancel: [id: string] }>();

const { t, locale } = useI18n();
const { timeHour12 } = useTimeFormat();
const now = useNow({ interval: 30_000 });

function sendsAt(p: ScheduledPrompt): string {
  const at = new Date(p.sendAt);
  return t("chat.schedule.sendsAt", {
    time: formatSendAt(at, now.value, locale.value, timeHour12.value),
    relative: formatUntil(at, now.value, locale.value),
  });
}
</script>

<template>
  <div
    v-if="prompts.length > 0"
    class="flex max-h-[min(30vh,14rem)] flex-col border-t border-border bg-[var(--paper-alt)] px-4 pb-1 pt-3"
    data-testid="scheduled-prompts-area"
  >
    <div
      class="mb-2 shrink-0 text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground"
    >
      {{ t("chat.schedule.header") }}
    </div>
    <div class="wy-scroll flex min-h-0 flex-col gap-1.5 overflow-y-auto pr-1">
      <div
        v-for="p in prompts"
        :key="p.id"
        class="group flex items-start gap-2 rounded-md border border-border bg-card px-3 py-2 text-foreground"
        data-testid="scheduled-prompt-card"
      >
        <Clock :size="13" class="mt-0.5 shrink-0 text-muted-foreground" />
        <div class="min-w-0 flex-1">
          <div
            v-if="p.content"
            class="whitespace-pre-wrap break-words text-[13px]"
            data-testid="scheduled-prompt-content"
          >
            {{ p.content }}
          </div>
          <div v-if="p.attachments?.length" class="mt-1.5 flex flex-wrap gap-1.5">
            <span
              v-for="attachment in p.attachments"
              :key="attachment.path"
              class="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded bg-muted px-2 py-1 text-[11px] text-muted-foreground"
              data-testid="scheduled-prompt-file"
            >
              <Paperclip class="size-3 shrink-0" />
              <span class="truncate">{{ attachment.name }}</span>
            </span>
          </div>
          <div
            class="mt-1 font-mono text-[10.5px] text-muted-foreground"
            data-testid="scheduled-prompt-time"
          >
            {{ sendsAt(p) }}
          </div>
        </div>
        <TooltipProvider :delay-duration="200">
          <Tooltip>
            <TooltipTrigger as-child>
              <button
                class="mt-0.5 flex size-5 shrink-0 cursor-pointer items-center justify-center rounded text-muted-foreground/70 transition-colors hover:bg-destructive/10 hover:text-destructive"
                :aria-label="t('chat.schedule.cancel')"
                data-testid="scheduled-prompt-cancel"
                @click="emit('cancel', p.id)"
              >
                <X :size="13" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="left">{{ t("chat.schedule.cancel") }}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
    </div>
  </div>
</template>
