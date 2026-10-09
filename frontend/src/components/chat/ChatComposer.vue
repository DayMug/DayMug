<script setup lang="ts">
// Composer stack: the staging area for queued prompts sitting directly on top
// of the input box. The two are always rendered together and measured as one
// block (the floating density toggle rides above their combined height), so
// they travel as a single component rather than being re-paired in each layout.
import { ref } from "vue";
import PendingPromptsArea from "./PendingPromptsArea.vue";
import ChatInput from "./ChatInput.vue";
import AskUserQuestionCard from "./AskUserQuestionCard.vue";
import type { PendingPrompt } from "@/composables/useChat";
import type { MessageAttachment } from "@/composables/useWebSocket";
import type { UserQuestionRequest } from "@/composables/useWebSocket";

defineProps<{
  prompts: PendingPrompt[];
  hasActiveTask: boolean;
  isThinking: boolean;
  turnStatusKnown: boolean;
  isConnected: boolean;
  conversationId: string;
  recallText: string;
  recallAttachments: MessageAttachment[];
  provider: string;
  imageInputUnsupported?: boolean;
  question?: UserQuestionRequest | null;
  answeringQuestion?: boolean;
}>();

const emit = defineEmits<{
  send: [text: string, attachments?: MessageAttachment[], insert?: boolean];
  cancel: [];
  "cancel-prompt": [id: string];
  "recall-consumed": [];
  answer: [requestId: string, answers: Record<string, string[]>];
}>();

const chatInputRef = ref<InstanceType<typeof ChatInput>>();

function forwardSend(text: string, attachments?: MessageAttachment[], insert?: boolean) {
  if (insert) {
    emit("send", text, attachments, true);
    return;
  }
  emit("send", text, attachments);
}

function forwardAnswer(requestId: string, answers: Record<string, string[]>) {
  emit("answer", requestId, answers);
}

// The page focuses the composer on mount; forwarding keeps that reach-through
// to one hop instead of the page walking into a grandchild.
defineExpose({ focus: () => chatInputRef.value?.focus() });
</script>

<template>
  <div class="flex max-h-full min-h-0 flex-col" data-testid="composer-stack">
    <AskUserQuestionCard
      v-if="question"
      class="min-h-0 flex-1"
      :request="question"
      :submitting="answeringQuestion"
      :is-connected="isConnected"
      @answer="forwardAnswer"
    />
    <PendingPromptsArea
      :prompts="prompts"
      :has-active-task="hasActiveTask"
      @cancel="emit('cancel-prompt', $event)"
    />
    <ChatInput
      ref="chatInputRef"
      :is-thinking="isThinking"
      :turn-status-known="turnStatusKnown"
      :is-connected="isConnected"
      :conversation-id="conversationId"
      :recall-text="recallText"
      :recall-attachments="recallAttachments"
      :provider="provider"
      :image-input-unsupported="imageInputUnsupported"
      @send="forwardSend"
      @cancel="emit('cancel')"
      @recall-consumed="emit('recall-consumed')"
    >
      <template #controls>
        <slot name="controls" />
      </template>
    </ChatInput>
  </div>
</template>
