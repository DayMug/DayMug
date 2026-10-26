<script setup lang="ts">
// The phone's only piece of top chrome. It has no border and no fill of its
// own: it sits on the same surface as the screen below it, so the page reads
// as one sheet with a few floating controls rather than a stack of bars.
//
// Two variants share it so the bar does not jump when moving between screens:
// - `conversation`: list button on the left, task activity and the artifacts
//   toggle on the right, and no title — the transcript is what the screen is
//   for, and the conversation's name is one tap away on the list. With
//   `floating` the bar takes no height at all: it lies over the transcript,
//   which scrolls up underneath it and is cut off cleanly behind the bar's
//   solid fill — a fade let half-transparent text show through on iOS.
// - `list`: product mark on the left, a shortcut back into the open
//   conversation in the centre, task activity and the account on the right.
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  BellOff,
  ChevronRight,
  Coffee,
  FolderOpen,
  Menu,
  MessageSquareText,
  RefreshCw,
} from "lucide-vue-next";
import TaskActivityButton from "./TaskActivityButton.vue";

withDefaults(
  defineProps<{
    variant?: "conversation" | "list";
    accountLabel: string;
    title?: string;
    floating?: boolean;
    notificationsEnabled: boolean;
    artifactsOpen?: boolean;
    canResume?: boolean;
  }>(),
  { variant: "conversation", title: "", floating: false, artifactsOpen: false, canResume: false },
);

const emit = defineEmits<{
  back: [];
  resume: [];
  "toggle-artifacts": [];
  "toggle-notifications": [];
  "open-account": [];
}>();

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

// Every control is a 44px target (the floor Apple and Google both set) with a
// smaller visible chip inside, so the bar stays light without being fiddly.
const target = "group grid size-11 shrink-0 place-items-center";
const chip =
  "grid size-9 place-items-center rounded-full border border-black/[0.06] bg-card text-[#3d4150] shadow-[0_1px_3px_rgba(20,20,30,0.06)] transition-colors group-active:bg-muted dark:border-white/10 dark:text-muted-foreground";
</script>

<template>
  <!-- On the list, side columns are equal (two 44px targets each) so the
       title is centred on the screen, not on whatever space one side leaves.
       Floating, the bar itself ignores touches so the transcript under its
       empty middle still scrolls; only the buttons take them. -->
  <header
    class="mobile-global-bar z-30 h-[52px] shrink-0 items-center gap-1 px-1.5"
    :class="[
      variant === 'list' ? 'grid grid-cols-[88px_minmax(0,1fr)_88px]' : 'flex justify-between',
      floating
        ? 'pointer-events-none absolute inset-x-0 top-0 bg-background [&_button]:pointer-events-auto'
        : 'relative',
    ]"
    :data-variant="variant"
  >
    <button
      v-if="variant === 'conversation'"
      type="button"
      data-testid="mobile-back-button"
      :class="target"
      :aria-label="t('sidebar.showConversations')"
      @click="emit('back')"
    >
      <span :class="chip"><Menu class="size-[18px]" /></span>
    </button>
    <span v-else class="grid size-11 place-items-center" data-testid="mobile-brand-mark">
      <span class="grid size-8 place-items-center rounded-[10px] bg-[#1f1f1f] text-white">
        <Coffee class="size-[17px]" />
      </span>
    </span>

    <button
      v-if="variant === 'list' && canResume"
      type="button"
      class="flex min-w-0 items-center justify-center gap-0.5 px-1 active:opacity-60"
      data-testid="mobile-header-title"
      @click="emit('resume')"
    >
      <span
        class="truncate text-[15px] font-semibold leading-5 tracking-[-0.01em] text-[#1f2023] dark:text-foreground"
        >{{ title }}</span
      >
      <ChevronRight class="size-3.5 shrink-0 text-muted-foreground" />
    </button>
    <span
      v-else-if="variant === 'list'"
      class="truncate px-1 text-center text-[15px] font-semibold leading-5 tracking-[-0.01em] text-[#1f2023] dark:text-foreground"
      data-testid="mobile-header-title"
      >{{ title }}</span
    >

    <div class="flex items-center justify-end">
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
      <!-- One button flips between the transcript and the artifacts; the icon
           names where it takes you, so it never shows the screen you are on. -->
      <button
        v-if="variant === 'conversation'"
        type="button"
        data-testid="mobile-artifacts-toggle"
        :class="target"
        :aria-label="artifactsOpen ? t('chat.mobileConversationTab') : t('chat.mobileArtifactsTab')"
        :aria-pressed="artifactsOpen"
        @click="emit('toggle-artifacts')"
      >
        <span :class="chip">
          <MessageSquareText v-if="artifactsOpen" class="size-[17px]" />
          <FolderOpen v-else class="size-[17px]" />
        </span>
      </button>
      <button
        v-else
        type="button"
        data-testid="mobile-account-button"
        :class="target"
        :aria-label="accountLabel"
        @click="emit('open-account')"
      >
        <span
          class="grid size-8 place-items-center rounded-full bg-[#25283b] text-[12px] font-bold leading-[18px] text-white"
        >
          {{ accountLabel.slice(0, 1).toUpperCase() }}
        </span>
      </button>
    </div>
  </header>
</template>
