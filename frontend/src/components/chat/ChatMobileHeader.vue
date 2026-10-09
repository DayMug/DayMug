<script setup lang="ts">
// Mobile global bar. It mirrors the reference workspace's stable top chrome:
// product mark on the left, the active conversation in the centre, and
// conversation list / task activity / account on the right. The content pane
// gets its own project header below this component.
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { Coffee, Menu, BellOff, RefreshCw } from "lucide-vue-next";
import TaskActivityButton from "./TaskActivityButton.vue";

defineProps<{
  backLabel: string;
  title: string;
  notificationsEnabled: boolean;
}>();

const emit = defineEmits<{ back: []; "toggle-notifications": []; "open-account": [] }>();

const { t } = useI18n();

const activity = ref<InstanceType<typeof TaskActivityButton>>();

// A hard reload is the escape hatch when the socket wedges; it deliberately
// bypasses the router so the whole app state is rebuilt. It lives in the task
// feed's footer because that is where a user looks when work seems stuck.
function reloadPage() {
  window.location.reload();
}

function enableNotifications() {
  activity.value?.close();
  emit("toggle-notifications");
}
</script>

<template>
  <header
    class="mobile-global-bar relative z-30 grid h-[54px] shrink-0 grid-cols-[48px_minmax(0,1fr)_auto] items-center border-b border-[#e5e8ef] bg-white dark:border-border dark:bg-card"
  >
    <button
      type="button"
      class="grid h-[54px] w-12 place-items-center"
      :aria-label="t('sidebar.showConversations')"
      data-testid="mobile-brand-button"
      @click="emit('back')"
    >
      <span
        class="grid size-[30px] place-items-center rounded-[9px] bg-[linear-gradient(145deg,#2b2b2b,#191919_58%,#3a3a3a)] text-white shadow-[0_6px_16px_rgba(25,25,25,0.22)]"
      >
        <Coffee class="size-[17px]" />
      </span>
    </button>
    <span
      class="truncate px-[5px] text-center text-[12px] font-bold leading-[18px] text-[#202124] dark:text-foreground"
    >
      {{ title }}
    </span>
    <div class="flex items-center pr-1">
      <button
        type="button"
        data-testid="mobile-back-button"
        class="group grid h-[54px] w-10 place-items-center text-[#626a7d] dark:text-muted-foreground"
        :aria-label="`${t('sidebar.showConversations')}: ${backLabel}`"
        @click="emit('back')"
      >
        <span class="grid size-9 place-items-center rounded-lg group-active:bg-muted">
          <Menu class="size-[19px]" />
        </span>
      </button>
      <TaskActivityButton ref="activity">
        <template #footer>
          <button
            v-if="!notificationsEnabled"
            type="button"
            class="flex h-9 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-lg text-[12px] text-[#626a7d] hover:bg-muted dark:text-muted-foreground"
            :title="t('conversations.notificationsSilencedTap')"
            data-testid="mobile-enable-notifications"
            @click="enableNotifications"
          >
            <BellOff class="size-3.5 shrink-0" />
            <span class="truncate">{{ t("conversations.notificationsSilencedTap") }}</span>
          </button>
          <button
            type="button"
            data-testid="mobile-refresh-page"
            class="flex h-9 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-lg text-[12px] text-[#626a7d] hover:bg-muted dark:text-muted-foreground"
            @click="reloadPage"
          >
            <RefreshCw class="size-3.5 shrink-0" />
            <span class="truncate">{{ t("common.refresh") }}</span>
          </button>
        </template>
      </TaskActivityButton>
      <button
        type="button"
        class="grid h-[54px] w-10 place-items-center"
        :aria-label="backLabel"
        @click="emit('open-account')"
      >
        <span
          class="grid size-[31px] place-items-center rounded-[10px] bg-[#25283b] text-[12px] font-bold leading-[18px] text-white"
        >
          {{ backLabel.slice(0, 1).toUpperCase() }}
        </span>
      </button>
    </div>
  </header>
</template>
