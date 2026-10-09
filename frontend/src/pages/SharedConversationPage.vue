<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import ChatTranscript from "@/components/chat/ChatTranscript.vue";
import { fetchSharedConversation } from "@/composables/apiConversations";
import { restMessageToChatMessage, type ChatMessage } from "@/composables/chat/messageState";
import { useChatRenderItems } from "@/composables/useChatRenderItems";
import type { Conversation } from "@/composables/apiTypes";

const route = useRoute();
const { t } = useI18n();

const loading = ref(true);
const error = ref("");
const conversation = ref<Conversation | null>(null);
const messages = ref<ChatMessage[]>([]);
const renderItems = useChatRenderItems(messages);

const title = computed(() => conversation.value?.title || t("conversations.new"));

onMounted(async () => {
  const token = String(route.params.token ?? "");
  try {
    const payload = await fetchSharedConversation(token);
    conversation.value = payload.conversation;
    messages.value = payload.messages.map(restMessageToChatMessage);
  } catch {
    error.value = t("conversations.shareUnavailable");
  } finally {
    loading.value = false;
  }
});
</script>

<template>
  <main class="h-full min-h-0 overflow-y-auto overflow-x-hidden bg-background text-foreground">
    <div class="mx-auto flex min-h-full w-full max-w-3xl flex-col px-4 py-6 sm:px-6">
      <header class="border-b border-border pb-4">
        <div class="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
          {{ t("conversations.sharedConversation") }}
        </div>
        <h1 class="mt-1 text-xl font-semibold tracking-tight">
          {{ title }}
        </h1>
      </header>

      <div v-if="loading" class="py-10 text-sm text-muted-foreground">
        {{ t("conversations.loadingShared") }}
      </div>
      <div v-else-if="error" class="py-10 text-sm text-destructive" role="alert">
        {{ error }}
      </div>
      <div v-else class="flex-1 py-5">
        <div v-if="messages.length === 0" class="py-10 text-sm text-muted-foreground">
          {{ t("conversations.emptyShort") }}
        </div>
        <ChatTranscript
          v-else
          :items="renderItems"
          :conversation-id="conversation?.id ?? ''"
          :is-empty="false"
          :is-loading-more-history="false"
          :is-thinking="false"
          :user-label="t('chat.you')"
          :assistant-label="t('conversations.sharedAssistant')"
          :can-show-clear-context="false"
          :clearing-context="false"
          :is-connected="false"
        />
      </div>
    </div>
  </main>
</template>
