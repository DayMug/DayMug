<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useAuth } from "@/composables/useAuth";
import { useUsers } from "@/composables/useUsers";
import AgentAvatar from "@/components/AgentAvatar.vue";
import {
  ArrowLeft,
  ChevronRight,
  Globe,
  KeyRound,
  Bell,
  CalendarClock,
  Info,
  Plus,
  BarChart3,
  LogOut,
  RotateCcw,
  Settings2,
  Shield,
} from "lucide-vue-next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { User } from "@/composables/useApi";
import { logout as apiLogout } from "@/composables/useApi";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { useIsMobile } from "@/composables/useIsMobile";
import { upgradeAvailable } from "@/stores/appChromeStore";
import { botPlatformLabelKey } from "@/lib/botPlatforms";

const router = useRouter();
const { t } = useI18n();
const { currentUser, users, selectUser, archivedAgents, loadArchivedAgents, handleRestoreUser } =
  useUsers();
const { authMe, clearAuthMe } = useAuth();
const isAdmin = computed(() => authMe.value?.is_admin === true);
// Desktop admins reach the Admin Panel from the sidebar account menu. Phones
// have no sidebar, so without this row the panel is unreachable there.
const { isMobile } = useIsMobile();
const agentUsers = computed(() => users.value.filter((user) => !user.username));
usePageTitle(computed(() => t("settings.titles.hub")));

function botPlatformsLabel(user: User): string {
  const labels = (user.bot_platforms ?? []).map((platform) => {
    const key = botPlatformLabelKey(platform);
    return key ? t(key) : platform;
  });
  return [...new Set(labels.filter(Boolean))].join(" · ");
}

// Archived agents are fetched on demand here — they're hidden from the sidebar
// and only surfaced for restore from this page.
onMounted(() => {
  void loadArchivedAgents();
});

async function selectAndEdit(id: string) {
  // Mirrors the legacy SettingsLayout behavior: prime app state with the
  // chosen agent so leaving settings drops the user back into that agent's
  // chat. The settings UI itself doesn't need it, but the chat sidebar does.
  await selectUser(id);
  router.push({ name: "settings-users-edit", params: { id } });
}

function backToChat() {
  router.push(currentUser.value ? { name: "chat", params: { userId: currentUser.value.id } } : "/");
}

const loggingOut = ref(false);
async function handleLogout() {
  if (loggingOut.value) return;
  loggingOut.value = true;
  try {
    await apiLogout();
  } catch {
    // Best-effort: server may already have invalidated the session; either way
    // we still want to clear local state and bounce to the login page.
  }
  clearAuthMe();
  router.push({ name: "login" });
}
</script>

<template>
  <div class="settings-hub mx-auto w-full max-w-[760px] px-4 py-6 sm:px-6 sm:py-10">
    <!-- Top bar: back-to-chat / title / sign-out. Shared by desktop and
         mobile; the entire settings flow now follows hub-and-spoke. -->
    <header class="mb-10 flex items-center gap-3 border-b pb-6">
      <Button
        variant="outline"
        size="icon"
        class="settings-hub-back size-9"
        :title="t('settings.backToChat')"
        @click="backToChat"
      >
        <ArrowLeft class="size-4" />
      </Button>
      <div class="flex-1">
        <div class="font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
          DayMug / {{ t("settings.title") }}
        </div>
        <h1 class="mt-1 text-2xl font-extrabold tracking-[-0.04em] text-foreground">
          {{ t("settings.title") }}
        </h1>
      </div>
      <Button
        variant="ghost"
        size="icon"
        class="settings-hub-signout size-9 text-muted-foreground"
        :title="loggingOut ? t('settings.signingOut') : t('settings.signOut')"
        :disabled="loggingOut"
        @click="handleLogout"
      >
        <LogOut class="size-4" />
      </Button>
    </header>

    <!-- System -->
    <section class="settings-hub-group mb-6">
      <h2 class="settings-hub-group-title">{{ t("settings.groups.system") }}</h2>
      <Card class="settings-hub-card">
        <Button
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-system' })"
        >
          <Globe class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.appearance") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-account' })"
        >
          <KeyRound class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.account") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-advanced' })"
        >
          <Settings2 class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.advanced") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-notifications' })"
        >
          <Bell class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.notifications") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row settings-hub-crontab"
          @click="router.push({ name: 'settings-crontab' })"
        >
          <CalendarClock class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.crontab") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          v-if="!isAdmin"
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-usage' })"
        >
          <BarChart3 class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.usage") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row"
          @click="router.push({ name: 'settings-about' })"
        >
          <Info class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.rows.about") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
      </Card>
    </section>

    <!-- Agents -->
    <section class="settings-hub-group mb-6">
      <h2 class="settings-hub-group-title">{{ t("settings.groups.agents") }}</h2>
      <Card class="settings-hub-card">
        <Button
          v-for="user in agentUsers"
          :key="user.id"
          variant="ghost"
          class="settings-hub-row settings-hub-agent-row"
          @click="selectAndEdit(user.id)"
        >
          <AgentAvatar
            :name="user.name"
            :avatar="user.avatar"
            class="size-6 shrink-0"
            fallback-class="text-[10px] font-bold"
          />
          <span class="flex-1 text-left truncate">{{ user.name }}</span>
          <Badge v-if="botPlatformsLabel(user)" variant="mono" class="text-[10px]">
            {{ botPlatformsLabel(user) }}
          </Badge>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
        <Button
          variant="ghost"
          class="settings-hub-row settings-hub-add-agent text-muted-foreground"
          @click="router.push({ name: 'settings-users-add' })"
        >
          <Plus class="size-4 shrink-0" />
          <span class="flex-1 text-left">{{ t("settings.rows.newAgent") }}</span>
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
      </Card>
    </section>

    <!-- Archived agents — only shown when the caller has archived at least one.
         Each row offers a one-click restore back into the sidebar. -->
    <section v-if="archivedAgents.length > 0" class="settings-hub-group mb-6">
      <h2 class="settings-hub-group-title">{{ t("settings.groups.archivedAgents") }}</h2>
      <Card class="settings-hub-card">
        <div
          v-for="user in archivedAgents"
          :key="user.id"
          class="settings-hub-row settings-hub-archived-row"
        >
          <AgentAvatar
            :name="user.name"
            :avatar="user.avatar"
            class="size-6 shrink-0 opacity-70"
            fallback-class="text-[10px] font-bold"
          />
          <span class="flex-1 text-left truncate text-muted-foreground">{{ user.name }}</span>
          <Badge v-if="botPlatformsLabel(user)" variant="mono" class="text-[10px]">
            {{ botPlatformsLabel(user) }}
          </Badge>
          <Button
            variant="ghost"
            size="sm"
            class="settings-hub-restore h-8 gap-1.5 px-2 text-[12px] text-muted-foreground"
            @click="handleRestoreUser(user.id)"
          >
            <RotateCcw class="size-3.5 shrink-0" />
            <span>{{ t("settings.rows.restoreAgent") }}</span>
          </Button>
        </div>
      </Card>
    </section>

    <section v-if="isAdmin && isMobile" class="settings-hub-group mb-6">
      <h2 class="settings-hub-group-title">{{ t("settings.groups.admin") }}</h2>
      <Card class="settings-hub-card">
        <Button
          variant="ghost"
          class="settings-hub-row settings-hub-admin-panel"
          @click="router.push({ name: 'settings-admin' })"
        >
          <Shield class="size-4 shrink-0 text-muted-foreground" />
          <span class="flex-1 text-left">{{ t("settings.titles.adminPanel") }}</span>
          <span
            v-if="upgradeAvailable"
            data-testid="settings-hub-admin-upgrade-badge"
            class="size-1.5 shrink-0 rounded-full bg-red-500"
            :title="t('sidebar.upgradeAvailable')"
          />
          <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
        </Button>
      </Card>
    </section>

    <!-- Identity footer -->
    <div
      v-if="authMe"
      class="settings-hub-identity flex flex-col items-center text-center gap-0.5 mt-8 pb-4"
    >
      <span class="text-[13px] font-medium text-foreground truncate max-w-full">
        {{ authMe.name || authMe.username }}
      </span>
      <span class="text-[12px] text-muted-foreground truncate max-w-full"
        >@{{ authMe.username }}</span
      >
    </div>
  </div>
</template>

<style scoped>
.settings-hub-group-title {
  margin: 0 0 0.625rem 0.125rem;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.16em;
  color: var(--muted-foreground);
}
.settings-hub-card {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding-block: 0;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--card);
  overflow: hidden;
  box-shadow: none;
}
.settings-hub-row {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  min-height: 3.25rem;
  height: auto;
  width: 100%;
  justify-content: flex-start;
  border-radius: 0;
  padding: 0.75rem 1rem;
  font-size: 14px;
  background: transparent;
  cursor: pointer;
  transition: background-color 120ms ease;
  text-align: left;
  color: var(--foreground);
}
.settings-hub-row + .settings-hub-row {
  border-top: 1px solid var(--border);
}
.settings-hub-row:hover {
  background: var(--accent);
}
</style>
