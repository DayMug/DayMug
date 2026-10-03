<script setup lang="ts">
import { computed, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { BarChart3, Boxes, ChevronRight, Settings2, SquareTerminal, Users } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useAuth } from "@/composables/useAuth";
import { usePageTitle } from "@/composables/useDocumentTitle";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";

const router = useRouter();
const { t } = useI18n();
const { authMe } = useAuth();
const isAdmin = computed(() => authMe.value?.is_admin === true);

usePageTitle(computed(() => t("settings.titles.adminPanel")));

// A direct URL is still subject to the server's admin middleware, but sending
// a known non-admin back to Settings avoids presenting an empty operator UI.
watch(
  authMe,
  (me) => {
    if (me && !me.is_admin) void router.replace({ name: "settings" });
  },
  { immediate: true },
);
</script>

<template>
  <div v-if="isAdmin" class="admin-panel mx-auto w-full max-w-[760px] px-4 pb-8 sm:px-6">
    <SettingsDetailHeader :title="t('settings.titles.adminPanel')" :eyebrow="t('settings.title')" />
    <p class="mb-5 text-[12px] text-muted-foreground">
      {{ t("settings.adminPanel.intro") }}
    </p>

    <Card class="admin-panel-card">
      <Button
        variant="ghost"
        class="admin-panel-row admin-panel-users"
        @click="router.push({ name: 'settings-admin-users' })"
      >
        <Users class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1 text-left">{{ t("settings.rows.adminUsers") }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
      </Button>
      <Button
        variant="ghost"
        class="admin-panel-row admin-panel-global"
        @click="router.push({ name: 'settings-admin-global' })"
      >
        <Settings2 class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1 text-left">{{ t("settings.rows.adminGlobal") }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
      </Button>
      <Button
        variant="ghost"
        class="admin-panel-row admin-panel-models"
        @click="router.push({ name: 'settings-admin-models' })"
      >
        <Boxes class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1 text-left">{{ t("settings.rows.adminModels") }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
      </Button>
      <Button
        variant="ghost"
        class="admin-panel-row admin-panel-terminal"
        @click="router.push({ name: 'settings-admin-terminal' })"
      >
        <SquareTerminal class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1 text-left">{{ t("settings.rows.adminTerminal") }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
      </Button>
      <Button
        variant="ghost"
        class="admin-panel-row admin-panel-usage"
        @click="router.push({ name: 'settings-usage' })"
      >
        <BarChart3 class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1 text-left">{{ t("settings.titles.usageAdmin") }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground/70" />
      </Button>
    </Card>
  </div>
</template>

<style scoped>
.admin-panel-card {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding-block: 0;
  overflow: hidden;
  border-radius: 16px;
  box-shadow: none;
}
.admin-panel-row {
  display: flex;
  width: 100%;
  min-height: 3.25rem;
  height: auto;
  align-items: center;
  justify-content: flex-start;
  gap: 0.625rem;
  border-radius: 0;
  padding: 0.75rem 1rem;
  color: var(--foreground);
  font-size: 14px;
}
.admin-panel-row + .admin-panel-row {
  border-top: 1px solid var(--border);
}
</style>
