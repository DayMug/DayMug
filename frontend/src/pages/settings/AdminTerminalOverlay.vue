<script setup lang="ts">
// Full-screen xterm overlay for an admin terminal session. Teleported to <body>
// so `position: fixed` resolves against the viewport, and sized with
// h-[100dvh] so it matches the visible area under a mobile browser bar. Kept
// mounted (v-show) so the xterm host node persists across sessions.
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { SquareTerminal } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import type { AdminTerminalSession } from "@/composables/useAdminTerminalSession";

const props = defineProps<{
  session: AdminTerminalSession;
  label: string;
}>();

const emit = defineEmits<{ close: [] }>();

const { t } = useI18n();
const termHost = ref<HTMLElement | null>(null);

onMounted(() => props.session.attachHost(termHost.value));

function close() {
  props.session.close();
  emit("close");
}
</script>

<template>
  <Teleport to="body">
    <div
      v-show="session.fullscreen.value"
      class="fixed inset-x-0 top-0 z-50 flex h-[100dvh] flex-col bg-background"
      data-testid="admin-terminal-overlay"
    >
      <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-card px-3">
        <SquareTerminal class="size-4 shrink-0 text-muted-foreground" />
        <span class="shrink-0 text-[13px] font-semibold">
          {{ t("settings.adminTerminal.outputHeading") }}
        </span>
        <span class="min-w-0 flex-1 truncate font-mono text-[12px] text-muted-foreground">
          {{ label }}
        </span>
        <span
          v-if="session.phase.value === 'exited'"
          class="shrink-0 text-[12px] text-muted-foreground"
        >
          {{ session.statusMsg.value }}
        </span>
        <Button size="sm" variant="outline" class="admin-terminal-close shrink-0" @click="close">
          {{ t("settings.adminTerminal.close") }}
        </Button>
      </div>
      <div
        ref="termHost"
        class="admin-terminal-output min-h-0 flex-1 overflow-hidden px-2 pb-6 pt-1"
      ></div>
    </div>
  </Teleport>
</template>

<style scoped>
.admin-terminal-output {
  color: var(--foreground);
}
</style>
