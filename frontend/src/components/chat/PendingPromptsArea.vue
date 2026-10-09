<script setup lang="ts">
import { computed } from "vue";
import { Paperclip, X } from "lucide-vue-next";
import type { PendingPrompt } from "@/composables/useChat";
import { displayMessageContent } from "@/composables/chat/messageState";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const props = defineProps<{
  prompts: PendingPrompt[];
  // True when the conversation has a Claude run actively executing
  // (status="thinking", not just queued in the per-account pool). Used
  // to surface the running task in the position label so the staging
  // area conveys "1 message ahead in this conversation" instead of a
  // bare "Queued" — which used to leave the user wondering what they
  // were queued behind.
  hasActiveTask?: boolean;
}>();

const emit = defineEmits<{
  // The string is either the persisted DB id (preferred) or the local
  // clientKey assigned at send time, for the brief window before
  // input_ack lands. The composable handles both forms.
  cancel: [idOrKey: string];
}>();

// Per-row queue position text. Rules in priority order:
//   - Index 0 with poolPosition > 0 → cross-account pool wait. Show its
//     position in the waiting queue so the user knows the wait isn't
//     from their own past prompts in this conversation.
//   - hasActiveTask true → fold the in-flight prompt into the count so
//     idx 0 reads as "1 message ahead", idx 1 as "2 messages ahead",
//     etc. Without this the head-of-staging would say "Queued" while
//     a Claude run was visibly thinking right above it.
//   - Index 0 without poolPosition and no active task → "Queued". We
//     deliberately do NOT show "Next up" here: when a queued prompt is
//     cancelled and resent, the new entry has no poolPosition info
//     until the next queue_status broadcast, and showing "Next up"
//     optimistically would lie when other waiters are actually ahead.
//   - Index N>0 → N prompts already queued in front of this one within
//     the conversation's own FIFO.
function aheadHint(idx: number, p: PendingPrompt): string {
  if (idx === 0 && p.poolPosition && p.poolPosition > 0) {
    const ahead = p.poolAhead ?? p.poolPosition;
    const running = p.poolRunning ?? 0;
    return t("chat.pending.accountBusy", {
      ahead,
      noun: t(ahead === 1 ? "chat.pending.message" : "chat.pending.messages"),
      running,
    });
  }
  const ahead = props.hasActiveTask ? idx + 1 : idx;
  if (ahead === 0) return t("chat.pending.queued");
  return t("chat.pending.aheadInConversation", {
    ahead,
    noun: t(ahead === 1 ? "chat.pending.message" : "chat.pending.messages"),
  });
}

// An IM prompt cannot be recalled into this composer — its text was typed in
// Slack or WeChat. Its × cancels the turn instead, so the affordance has to say
// so rather than promising an edit that will not happen.
function cancelLabel(p: PendingPrompt): string {
  return p.fromIM ? t("chat.pending.cancelIM") : t("chat.pending.recall");
}

function promptContent(p: PendingPrompt): string {
  return displayMessageContent(p.content, p.sender, p.attachments);
}

const items = computed(() => props.prompts);
</script>

<template>
  <div
    v-if="items.length > 0"
    class="pending-prompts-area flex max-h-[min(40vh,18rem)] flex-col px-4 pt-3 pb-1 border-t border-border bg-[var(--paper-alt)]"
    data-testid="pending-prompts-area"
  >
    <div
      class="mb-2 shrink-0 text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground"
    >
      {{ items.some((p) => p.fromIM) ? t("chat.pending.headerIM") : t("chat.pending.header") }}
    </div>
    <div class="wy-scroll flex min-h-0 flex-col gap-1.5 overflow-y-auto pr-1">
      <div
        v-for="(p, i) in items"
        :key="p.id || p.clientKey"
        class="pending-prompt-card group flex items-start gap-2 px-3 py-2 rounded-md border border-border bg-card text-foreground"
        data-testid="pending-prompt-card"
      >
        <div class="flex-1 min-w-0">
          <div
            v-if="promptContent(p)"
            class="text-[13px] whitespace-pre-wrap break-words"
            data-testid="pending-prompt-content"
          >
            {{ promptContent(p) }}
          </div>
          <div
            v-if="p.attachments?.length"
            class="mt-1.5 flex flex-wrap gap-1.5"
            data-testid="pending-prompt-attachments"
          >
            <template v-for="attachment in p.attachments" :key="attachment.path">
              <img
                v-if="attachment.mime.startsWith('image/')"
                :src="attachment.url"
                :alt="attachment.name"
                loading="lazy"
                class="block max-h-24 max-w-40 rounded border border-border object-contain"
                data-testid="pending-prompt-image"
              />
              <span
                v-else
                class="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded bg-muted px-2 py-1 text-[11px] text-muted-foreground"
                data-testid="pending-prompt-file"
              >
                <Paperclip class="size-3 shrink-0" />
                <span class="truncate">{{ attachment.name }}</span>
              </span>
            </template>
          </div>
          <div
            class="mt-1 text-[10.5px] font-mono text-muted-foreground"
            data-testid="pending-prompt-position"
          >
            {{ aheadHint(i, p) }}
          </div>
        </div>
        <TooltipProvider :delay-duration="200">
          <Tooltip>
            <TooltipTrigger as-child>
              <button
                class="shrink-0 mt-0.5 size-5 flex items-center justify-center rounded text-muted-foreground/70 hover:text-destructive hover:bg-destructive/10 transition-colors cursor-pointer"
                :aria-label="cancelLabel(p)"
                data-testid="pending-prompt-cancel"
                @click="emit('cancel', p.id || p.clientKey)"
              >
                <X :size="13" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="left">{{ cancelLabel(p) }}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
    </div>
  </div>
</template>
