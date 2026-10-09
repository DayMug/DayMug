<script setup lang="ts">
import { computed } from "vue";
import { FolderOpen, MessageSquareText, Sparkles } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { conversationAttention } from "@/stores/conversationAttentionStore";

defineProps<{
  active: "sessions" | "chat" | "artifacts";
}>();

const emit = defineEmits<{ sessions: []; chat: []; artifacts: [] }>();
const { t } = useI18n();

// The badge counts conversations that want the user back — unread results,
// failures and pending questions — not the size of the list, which only grows
// and so stopped meaning anything once it reached double digits.
const attentionCount = computed(() => Object.keys(conversationAttention.value).length);

const tabClass =
  "relative flex min-h-12 min-w-0 flex-col items-center justify-center gap-0.5 rounded-[9px] text-[11px] leading-[16.5px] transition-colors";
const activeClass = "bg-[#f3f2ff] text-[#242424] dark:bg-[#27233d] dark:text-white";
const idleClass = "text-[#8b91a1] dark:text-muted-foreground";
</script>

<template>
  <nav
    class="mobile-workspace-nav grid h-[calc(52px+env(safe-area-inset-bottom))] shrink-0 grid-cols-3 border-t border-[#e5e8ef] bg-white/[0.96] px-[max(9px,env(safe-area-inset-left))] pb-[env(safe-area-inset-bottom)] pt-[3px] backdrop-blur-[18px] dark:border-border dark:bg-card/95"
    :aria-label="t('chat.mobileNavigation')"
  >
    <button
      type="button"
      :class="[tabClass, active === 'sessions' ? activeClass : idleClass]"
      data-testid="mobile-conversations-tab"
      @click="emit('sessions')"
    >
      <MessageSquareText class="size-[19px]" />
      <span>{{ t("chat.mobileConversationsTab") }}</span>
      <i
        v-if="attentionCount"
        data-testid="mobile-conversations-badge"
        class="absolute right-[calc(50%-18px)] top-[3px] grid h-[15px] min-w-[15px] place-items-center rounded-lg border-2 border-white bg-[#ef6c50] px-[3px] text-[7px] not-italic leading-none text-white dark:border-card"
      >
        {{ attentionCount > 99 ? "99+" : attentionCount }}
      </i>
    </button>
    <button
      type="button"
      :class="[tabClass, active === 'chat' ? activeClass : idleClass]"
      data-testid="mobile-chat-tab"
      @click="emit('chat')"
    >
      <Sparkles class="size-[19px]" />
      <span>{{ t("chat.mobileConversationTab") }}</span>
    </button>
    <button
      type="button"
      :class="[tabClass, active === 'artifacts' ? activeClass : idleClass]"
      data-testid="mobile-artifacts-tab"
      @click="emit('artifacts')"
    >
      <FolderOpen class="size-[19px]" />
      <span>{{ t("chat.mobileArtifactsTab") }}</span>
    </button>
  </nav>
</template>
