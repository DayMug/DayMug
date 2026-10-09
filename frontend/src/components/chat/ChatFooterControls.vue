<script setup lang="ts">
import ModelSelector from "@/components/ModelSelector.vue";
import type { Conversation } from "@/composables/apiTypes";
import type { ContextUsage, RateLimitInfo, RateLimitWindow } from "@/stores/chatContextStore";
import ContextUsageBar from "./ContextUsageBar.vue";
import RateLimitBadge from "./RateLimitBadge.vue";

defineProps<{
  conversation: Conversation | null;
  conversationStarted: boolean;
  providerAccounts?: Record<string, string[]>;
  error?: string;
  showContextUsage: boolean;
  contextUsage: ContextUsage | null;
  provider: string;
  usageSummary?: string;
  showRateLimit: boolean;
  rateLimits: Partial<Record<RateLimitWindow, RateLimitInfo>>;
}>();

const emit = defineEmits<{
  updated: [conversation: Conversation];
  error: [message: string];
}>();
</script>

<template>
  <!-- `contents`, not a nested flex row: the selector becomes a direct flex
       item of the composer's unwrapped control row, so its label can truncate
       while staying on the same line as Send. A shrinkable wrapper previously
       collapsed the selector itself to zero width on phones. -->
  <div
    v-if="conversation || showContextUsage || showRateLimit"
    data-testid="chat-footer-controls"
    class="contents"
  >
    <ModelSelector
      v-if="conversation"
      class="min-w-0"
      :conversation="conversation"
      :conversation-started="conversationStarted"
      :provider-accounts="providerAccounts"
      @updated="emit('updated', $event)"
      @error="emit('error', $event)"
    />
    <span v-if="error" class="min-w-0 truncate text-[11px] text-destructive" :title="error">{{
      error
    }}</span>
    <div v-if="showContextUsage || showRateLimit" class="flex shrink-0 items-center gap-2">
      <ContextUsageBar
        v-if="showContextUsage && contextUsage"
        :used="contextUsage.used"
        :total="contextUsage.total"
        :input-tokens="contextUsage.input_tokens"
        :cache-read="contextUsage.cache_read"
        :cache-creation="contextUsage.cache_creation"
        :price-breakpoint="provider === 'codex' ? 272000 : undefined"
        :usage-summary="usageSummary"
      />
      <RateLimitBadge v-if="showRateLimit" :rate-limits="rateLimits" class="shrink-0" />
    </div>
  </div>
</template>
