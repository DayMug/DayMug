<script setup lang="ts">
// Shell for the global admin page. Every section that owns state or talks to
// the server lives in its own child component; what stays here is the public
// config fetch plus the two read-only mirrors that are a single <code> block
// each (home root, sandbox) and would only gain indirection from extraction.
import { computed, onMounted, ref } from "vue";
import { adminFetchPublicConfig } from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import type { AdminPublicConfig } from "@/composables/useApi";
import { useI18n } from "vue-i18n";
import AdminGlobalUpgrade from "./AdminGlobalUpgrade.vue";
import AdminGlobalPauseTasks from "./AdminGlobalPauseTasks.vue";
import AdminGlobalDatabase from "./AdminGlobalDatabase.vue";
import AdminGlobalHelpDoc from "./AdminGlobalHelpDoc.vue";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.titles.adminGlobal")));

const cfg = ref<AdminPublicConfig | null>(null);
// Destructured with the template's historical names so bindings (and the
// page tests that assert on them) stay unchanged.
const { error: errorMsg, run: runLoadConfig } = useAsyncOperation();

const upgradeEnabled = computed(() => !!cfg.value?.upgrade.enabled);

// SQLite is the only backend that needs an explicit VACUUM to reclaim space
// from deleted rows. Any future engine the server supports will hide the
// section by reporting a different `backend` value — and because the guard
// sits on the child's v-if, its on-mount size fetch never fires either.
const canOptimizeDB = computed(() => cfg.value?.backend === "sqlite");

onMounted(async () => {
  cfg.value = (await runLoadConfig(() => adminFetchPublicConfig())) ?? null;
});

// The upgrade panel owns the watchdog polling but not the public config, so
// it tells us when the new version is live and we refetch the header value.
async function onUpgradeSucceeded() {
  try {
    cfg.value = await adminFetchPublicConfig();
  } catch {
    // ignore — header just keeps showing the previous version.
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[760px] px-4 pb-8">
    <SettingsDetailHeader
      :title="t('settings.adminGlobal.title')"
      :eyebrow="t('settings.adminGlobal.eyebrow')"
      parent-route-name="settings-admin"
    />
    <p class="text-[12px] text-muted-foreground mb-5">
      {{ t("settings.adminGlobal.intro") }}
    </p>

    <p v-if="errorMsg" class="mb-3 text-[12px] text-red-600">{{ errorMsg }}</p>

    <!-- Children mount only once the config resolves, which is what keeps the
         self-fetching sections (pricing, help doc) from hitting the server on
         a page that failed to load its config. -->
    <div v-if="cfg" class="space-y-5">
      <AdminGlobalUpgrade
        :current-version="cfg.current_version"
        :enabled="upgradeEnabled"
        @upgrade-succeeded="onUpgradeSucceeded"
      />

      <!-- Directly under the upgrade panel: this is the switch you flip
           immediately before running one. -->
      <AdminGlobalPauseTasks />

      <section class="border border-border rounded-md bg-card p-4">
        <h2 class="text-[14px] font-semibold mb-2">
          {{ t("settings.adminGlobal.defaultHomeRoot") }}
        </h2>
        <p class="text-[12px] text-muted-foreground mb-2">
          {{ t("settings.adminGlobal.defaultHomeRootIntro", { root: "{this}" }) }}
        </p>
        <code class="block px-2 py-1 bg-accent/50 rounded text-[12px] font-mono break-all">
          {{ cfg.default_home_root }}
        </code>
      </section>

      <section class="border border-border rounded-md bg-card p-4">
        <h2 class="text-[14px] font-semibold mb-2">{{ t("settings.adminGlobal.sandbox") }}</h2>
        <p class="text-[12px] text-muted-foreground">
          {{ t("settings.adminGlobal.sandboxType") }}
          <code class="px-1 py-0.5 bg-accent rounded text-[11px]">{{ cfg.sandbox.type }}</code
          >,
          <span v-if="cfg.sandbox.enabled" class="text-green-700 font-medium">{{
            t("settings.adminGlobal.enabled")
          }}</span>
          <span v-else class="text-muted-foreground italic">{{
            t("settings.adminGlobal.disabled")
          }}</span
          >.
        </p>
      </section>

      <AdminGlobalDatabase v-if="canOptimizeDB" />

      <AdminGlobalHelpDoc />
    </div>
  </div>
</template>
