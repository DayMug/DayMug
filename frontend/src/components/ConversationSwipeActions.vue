<script setup lang="ts">
import { Link2Off, Share2, Trash2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { SWIPE_ACTION_WIDTH, SWIPE_WIDTH } from "@/composables/useConversationGestures";

defineProps<{
  shared?: boolean;
}>();

const emit = defineEmits<{
  share: [];
  delete: [];
}>();

const { t } = useI18n();
</script>

<template>
  <div
    class="swipe-actions absolute inset-y-0 right-0 z-0 flex"
    :style="{ width: `${SWIPE_WIDTH}px` }"
  >
    <button
      class="swipe-action swipe-share"
      :style="{ width: `${SWIPE_ACTION_WIDTH}px` }"
      :aria-label="shared ? t('conversations.unshare') : t('conversations.share')"
      @click.stop="emit('share')"
    >
      <span class="swipe-action-icon" aria-hidden="true">
        <Link2Off v-if="shared" />
        <Share2 v-else />
      </span>
      <span>{{ shared ? t("conversations.unshare") : t("conversations.share") }}</span>
    </button>
    <button
      class="swipe-action swipe-delete"
      :style="{ width: `${SWIPE_ACTION_WIDTH}px` }"
      :aria-label="t('conversations.deleteTitle')"
      @click.stop="emit('delete')"
    >
      <span class="swipe-action-icon" aria-hidden="true">
        <Trash2 />
      </span>
      <span>{{ t("conversations.delete") }}</span>
    </button>
  </div>
</template>

<style scoped>
.swipe-actions {
  gap: 4px;
  padding: 4px 0 4px 4px;
}

.swipe-action {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  border: 1px solid var(--border);
  border-radius: calc(var(--radius) - 2px);
  background: var(--card);
  font-size: 10px;
  font-weight: 600;
  line-height: 1;
  transition:
    background-color 150ms ease,
    transform 150ms ease;
}

.swipe-action:active {
  transform: scale(0.96);
}

.swipe-action-icon {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: 999px;
}

.swipe-action-icon :deep(svg) {
  width: 15px;
  height: 15px;
  stroke-width: 2;
}

.swipe-share {
  color: var(--primary);
}

.swipe-share .swipe-action-icon {
  background: color-mix(in oklch, var(--primary) 12%, transparent);
}

.swipe-delete {
  border-color: color-mix(in oklch, var(--swipe-delete) 24%, var(--border));
  color: var(--swipe-delete);
}

.swipe-delete .swipe-action-icon {
  background: color-mix(in oklch, var(--swipe-delete) 12%, transparent);
}

@media (prefers-reduced-motion: reduce) {
  .swipe-action {
    transition: none;
  }
}
</style>
