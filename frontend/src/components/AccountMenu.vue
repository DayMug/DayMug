<script setup lang="ts">
import { computed, ref } from "vue";
import {
  Activity,
  ChevronRight,
  HelpCircle,
  LayoutGrid,
  MoreHorizontal,
  Settings2,
  ShieldCheck,
} from "lucide-vue-next";
import {
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuRoot,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "reka-ui";
import { useI18n } from "vue-i18n";
import type { User } from "@/composables/useApi";
import type { RunningConversationActivity } from "@/stores/agentActivityStore";
import RunningActivityTable from "@/components/RunningActivityTable.vue";

const props = defineProps<{
  name: string;
  username?: string;
  helpAvailable?: boolean;
  upgradeAvailable?: boolean;
  isAdmin?: boolean;
  users?: User[];
  runningConversations?: RunningConversationActivity[];
  queuedConversations?: RunningConversationActivity[];
}>();

const emit = defineEmits<{
  "open-marketplace": [];
  "open-help": [];
  "open-settings": [];
  "open-admin-settings": [];
}>();

const { t } = useI18n();
const open = ref(false);
const activityOpenedAt = ref(Date.now());
const activityCount = computed(
  () => (props.runningConversations?.length ?? 0) + (props.queuedConversations?.length ?? 0),
);

function handleActivityOpen(value: boolean) {
  if (value) activityOpenedAt.value = Date.now();
}

const itemClass =
  "flex h-9 cursor-pointer items-center gap-2 rounded-[8px] px-2 text-[13px] text-[#202124] outline-none select-none data-[highlighted]:bg-[#f1f1f2] dark:text-[#ececec] dark:data-[highlighted]:bg-[#2a2a2a] [&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:stroke-[1.75]";
</script>

<template>
  <!-- Non-modal: the rest of the sidebar stays scrollable and hoverable while
       the menu is up, matching how the Iris account popover floats. -->
  <DropdownMenuRoot v-model:open="open" :modal="false">
    <DropdownMenuTrigger as-child>
      <button
        type="button"
        class="relative grid size-8 shrink-0 place-items-center rounded-[10px] text-[#4a4a4a] transition-colors outline-none hover:bg-[#e9e9e9] focus-visible:bg-[#e9e9e9] data-[state=open]:bg-[#e9e9e9] dark:text-[#aaa] dark:hover:bg-[#252525] dark:focus-visible:bg-[#252525] dark:data-[state=open]:bg-[#252525]"
        :aria-label="upgradeAvailable ? t('sidebar.upgradeAvailable') : t('sidebar.accountMenu')"
        data-testid="account-menu-trigger"
      >
        <MoreHorizontal class="size-[17px]" aria-hidden="true" />
        <span
          v-if="upgradeAvailable"
          data-testid="account-menu-upgrade-badge"
          aria-hidden="true"
          class="absolute right-1 top-1 size-2 rounded-full bg-red-500 ring-2 ring-[#f7f7f7] dark:ring-[#151515]"
        />
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuPortal>
      <!-- Opens upwards from the trigger and hangs off its left edge, so it
           reads as belonging to the footer rather than to the chat it overlaps. -->
      <DropdownMenuContent
        side="top"
        align="start"
        :side-offset="2"
        :align-offset="-8"
        class="z-50 flex w-[238px] flex-col gap-0.5 rounded-[13px] border border-[#dedee0] bg-white/98 p-3 text-[#202124] shadow-[0_18px_52px_rgb(19_19_22/0.14),0_2px_8px_rgb(19_19_22/0.06)] backdrop-blur-[18px] data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[side=top]:slide-in-from-bottom-2 dark:border-[#303030] dark:bg-[#1c1c1c]/98 dark:text-[#ececec]"
        data-testid="account-menu"
      >
        <div class="flex min-w-0 flex-col">
          <strong class="truncate text-[13px] font-bold leading-5">{{ name }}</strong>
          <small
            v-if="username && username !== name"
            class="mb-2.5 mt-0.5 truncate text-[11px] leading-[17px] text-[#8c8d92]"
            >@{{ username }}</small
          >
          <span v-else class="h-2.5" aria-hidden="true" />
        </div>
        <DropdownMenuItem
          :class="itemClass"
          data-testid="account-menu-marketplace"
          @select="emit('open-marketplace')"
        >
          <LayoutGrid aria-hidden="true" />
          <span>{{ t("sidebar.marketplace") }}</span>
        </DropdownMenuItem>
        <DropdownMenuItem
          v-if="helpAvailable"
          :class="itemClass"
          data-testid="account-menu-help"
          @select="emit('open-help')"
        >
          <HelpCircle aria-hidden="true" />
          <span>{{ t("sidebar.help") }}</span>
        </DropdownMenuItem>
        <!-- Lives here rather than on the agent rail while the conversation
             panel is docked: the account footer overlaps the rail's bottom
             edge and used to cover the activity icon. -->
        <DropdownMenuSub @update:open="handleActivityOpen">
          <DropdownMenuSubTrigger
            :class="[
              itemClass,
              'data-[state=open]:bg-[#f1f1f2] dark:data-[state=open]:bg-[#2a2a2a]',
            ]"
            data-testid="account-menu-activity"
          >
            <Activity aria-hidden="true" />
            <span class="flex-1">{{ t("sidebar.activity") }}</span>
            <span
              v-if="activityCount > 0"
              class="text-[12px] text-[#8c8d92] tabular-nums"
              data-testid="account-menu-activity-count"
              >{{ activityCount }}</span
            >
            <ChevronRight class="text-[#8c8d92]" aria-hidden="true" />
          </DropdownMenuSubTrigger>
          <DropdownMenuPortal>
            <DropdownMenuSubContent
              :side-offset="10"
              class="z-50 w-[440px] max-w-[calc(100vw-5rem)] rounded-[6px] border border-white/15 bg-neutral-900 text-left text-xs leading-snug text-white shadow-md data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
              data-inverted-surface
              data-testid="running-conversations-popup"
            >
              <RunningActivityTable
                :users="users ?? []"
                :running-conversations="runningConversations ?? []"
                :queued-conversations="queuedConversations ?? []"
                :opened-at="activityOpenedAt"
              />
            </DropdownMenuSubContent>
          </DropdownMenuPortal>
        </DropdownMenuSub>
        <DropdownMenuItem
          :class="itemClass"
          data-testid="account-menu-settings"
          @select="emit('open-settings')"
        >
          <Settings2 aria-hidden="true" />
          <span>{{ t("sidebar.settings") }}</span>
        </DropdownMenuItem>
        <DropdownMenuItem
          v-if="isAdmin"
          :class="itemClass"
          data-testid="account-menu-admin-settings"
          @select="emit('open-admin-settings')"
        >
          <ShieldCheck aria-hidden="true" />
          <span class="flex-1">{{ t("sidebar.adminSettings") }}</span>
          <span
            v-if="upgradeAvailable"
            data-testid="account-menu-admin-upgrade-badge"
            class="size-1.5 rounded-full bg-red-500"
            :title="t('sidebar.upgradeAvailable')"
          />
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>
