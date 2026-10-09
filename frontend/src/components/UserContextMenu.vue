<script setup lang="ts">
import { onMounted, onUnmounted, ref, nextTick } from "vue";
import type { User } from "@/composables/useApi";
import AgentAvatar from "@/components/AgentAvatar.vue";
import { Pencil, Archive, Trash2 } from "lucide-vue-next";
import { clampMenuPosition } from "@/lib/menuPosition";
import { useI18n } from "vue-i18n";

const props = defineProps<{
  user: User;
  x: number;
  y: number;
}>();

const { t } = useI18n();

const emit = defineEmits<{
  close: [];
  edit: [user: User];
  archive: [user: User];
  cleanup: [user: User];
}>();

const menuRef = ref<HTMLElement | null>(null);
const pos = ref({ left: props.x, top: props.y });

function handleClickOutside(e: MouseEvent) {
  const el = (e.target as HTMLElement).closest(".context-menu");
  if (!el) emit("close");
}

onMounted(() => {
  document.addEventListener("mousedown", handleClickOutside);
  nextTick(() => {
    if (menuRef.value) {
      pos.value = clampMenuPosition(menuRef.value, props.x, props.y);
    }
  });
});

onUnmounted(() => {
  document.removeEventListener("mousedown", handleClickOutside);
});

// Truncated agent id used as the meta line, mono — UI design ships
// "agt_xxxx" right of the avatar/name.
const agentShortId = `agt_${props.user.id.slice(0, 4)}`;
</script>

<template>
  <!-- Floating context menu — UI design `ContextMenu` atom. Header card
       (avatar + name + agt_id mono) sits above a divider, then the two
       agent actions (edit / archive), each a leading icon + label. -->
  <div
    ref="menuRef"
    class="context-menu fixed z-[1000] min-w-[240px] max-w-[calc(100vw-1rem)] bg-popover text-popover-foreground border border-rule rounded-xl shadow-2xl p-1.5 text-[13px]"
    :style="{ left: pos.left + 'px', top: pos.top + 'px' }"
  >
    <!-- Header card -->
    <div class="flex items-center gap-2 px-2.5 py-2 mb-1 border-b border-[var(--edge-soft)]">
      <AgentAvatar
        :name="props.user.name"
        :avatar="props.user.avatar"
        class="ctx-avatar size-5 shrink-0"
        fallback-class="text-[9px] font-bold"
      />
      <div class="ctx-name font-semibold text-foreground truncate flex-1">
        {{ props.user.name }}
      </div>
      <span class="text-[10px] font-mono text-muted-foreground">{{ agentShortId }}</span>
    </div>

    <!-- Items -->
    <button
      class="ctx-btn edit w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md cursor-pointer text-foreground hover:bg-[var(--paper-alt)] transition-colors"
      @click="emit('edit', props.user)"
    >
      <Pencil class="size-3.5 text-muted-foreground" />
      <span class="flex-1 text-left">{{ t("agentMenu.edit") }}</span>
    </button>
    <button
      class="ctx-btn cleanup w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md cursor-pointer text-destructive hover:bg-[var(--paper-alt)] transition-colors"
      @click="emit('cleanup', props.user)"
    >
      <Trash2 class="size-3.5" />
      <span class="flex-1 text-left">{{ t("agentMenu.cleanupStale") }}</span>
    </button>
    <button
      class="ctx-btn archive w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md cursor-pointer text-foreground hover:bg-[var(--paper-alt)] transition-colors"
      @click="emit('archive', props.user)"
    >
      <Archive class="size-3.5 text-muted-foreground" />
      <span class="flex-1 text-left">{{ t("agentMenu.archive") }}</span>
    </button>
  </div>
</template>
