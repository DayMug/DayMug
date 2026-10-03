<script setup lang="ts">
// Transcript body: history spinner, empty state, message/tool-group stream,
// thinking indicator and the inline "clear context" trigger.
//
// Rendered inside the scroll container rather than owning it — the container's
// padding differs between the mobile and desktop layouts, and the parent needs
// the element itself for scroll-driven history paging.
//
// Owns the fold state for thinking blocks and tool groups: pure view state,
// keyed by the render plan's per-message stable key so that prepending older
// history or switching density leaves each row folded the way the user left
// it. The component itself is never unmounted on the desktop layout, so the
// state is cleared explicitly when the conversation changes.
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Hourglass } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import ChatMessageItem from "./ChatMessageItem.vue";
import ChatToolGroup from "./ChatToolGroup.vue";
import type { RenderItem, ToolGroupItem } from "@/composables/useChatRenderItems";

const props = defineProps<{
  items: RenderItem[];
  /** Active conversation; a change drops the fold state of the previous one. */
  conversationId: string;
  /** True when the conversation has no messages at all (pre-density-filter). */
  isEmpty: boolean;
  isLoadingMoreHistory: boolean;
  isThinking: boolean;
  /** Turn finished, agent parked on background work it will wake from. */
  isWaitingOnBackground?: boolean;
  userLabel: string;
  assistantLabel: string;
  canShowClearContext: boolean;
  clearingContext: boolean;
  isConnected: boolean;
}>();

const emit = defineEmits<{ "clear-context": [] }>();

const { t } = useI18n();

// Finished thinking folds into its one-line summary by default (Iris); a live
// pass stays open so the reasoning is readable while it streams. An explicit
// toggle wins over either default and survives the live → finished flip.
const thinkingOverrides = ref<Map<string, boolean>>(new Map());
const expandedToolGroups = ref<Set<string>>(new Set());

watch(
  () => props.conversationId,
  () => {
    thinkingOverrides.value.clear();
    expandedToolGroups.value.clear();
  },
);

function isThinkingCollapsed(key: string, msg: { activityType?: string; id?: string }): boolean {
  const override = thinkingOverrides.value.get(key);
  if (override !== undefined) return override;
  return msg.activityType === "thinking" && !!msg.id;
}

function toggleThinking(key: string, msg: { activityType?: string; id?: string }) {
  thinkingOverrides.value.set(key, !isThinkingCollapsed(key, msg));
}

// A group is anchored on its first tool, but a prepended history page can end
// mid-run and merge into it, moving the anchor. Treating "any member is in the
// set" as expanded keeps such a group open; collapsing then has to clear every
// member so a stale anchor can't re-open it.
function isToolGroupExpanded(group: ToolGroupItem): boolean {
  return group.tools.some((tool) => expandedToolGroups.value.has(tool.key));
}

function toggleToolGroup(group: ToolGroupItem) {
  if (isToolGroupExpanded(group)) {
    for (const tool of group.tools) expandedToolGroups.value.delete(tool.key);
  } else {
    expandedToolGroups.value.add(group.key);
  }
}
</script>

<template>
  <div class="chat-thread mx-auto w-full max-w-[760px]">
    <div v-if="isLoadingMoreHistory" class="text-center text-muted-foreground text-[12px] py-2">
      {{ t("chat.loadingEarlier") }}
    </div>
    <div v-if="isEmpty" class="text-muted-foreground text-center py-10 px-4 text-sm">
      {{ t("chat.sendStart", { name: assistantLabel }) }}
    </div>
    <template v-for="(item, index) in items" :key="item.key">
      <ChatToolGroup
        v-if="item.kind === 'tool-group'"
        :class="{ 'chat-row-deferrable': index < items.length - 1 }"
        :tools="item.tools"
        :subagent="item.subagent"
        :expanded="isToolGroupExpanded(item)"
        @toggle="toggleToolGroup(item)"
      />
      <ChatMessageItem
        v-else
        :msg="item.msg"
        :collapsed="isThinkingCollapsed(item.key, item.msg)"
        :defer-offscreen="index < items.length - 1"
        :user-label="userLabel"
        :assistant-label="assistantLabel"
        @toggle-thinking="toggleThinking(item.key, item.msg)"
      />
    </template>
    <div
      v-if="isThinking"
      class="thinking-indicator flex items-center gap-2 text-muted-foreground text-[13px] py-2"
    >
      <span class="inline-block w-1.5 h-1.5 rounded-full bg-primary animate-pulse"></span>
      <span>{{ t("chat.assistantThinking", { name: assistantLabel }) }}</span>
      <span class="thinking-dots" aria-hidden="true">
        <span>.</span><span>.</span><span>.</span>
      </span>
    </div>
    <div
      v-else-if="isWaitingOnBackground"
      data-testid="background-wait-indicator"
      class="flex items-center gap-2 text-muted-foreground text-[13px] py-2"
    >
      <Hourglass class="size-3.5 shrink-0" aria-hidden="true" />
      <span>{{ t("chat.waitingOnBackground", { name: assistantLabel }) }}</span>
    </div>
    <!-- Rotates the underlying session id so the next message starts a fresh CLI
       process with no prior context, while keeping the thread visible. Distinct
       from "New session", which forks an entirely new conversation. -->
    <div v-if="canShowClearContext" class="flex justify-center pt-3 pb-1">
      <Button
        type="button"
        data-testid="clear-context-btn"
        variant="outline"
        size="sm"
        class="h-7 rounded-full px-3 text-[12px] font-normal text-muted-foreground"
        :disabled="clearingContext || !isConnected"
        :title="
          isConnected
            ? t('chat.clearContextConnectedTitle')
            : t('chat.clearContextDisconnectedTitle')
        "
        @click="emit('clear-context')"
      >
        {{ clearingContext ? t("chat.clearingContext") : t("chat.clearContext") }}
      </Button>
    </div>
  </div>
</template>
