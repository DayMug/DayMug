<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminFetchPublicConfig, type AdminPublicConfig } from "@/composables/useApi";
import { Button } from "@/components/ui/button";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import AdminTerminalOverlay from "./AdminTerminalOverlay.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { useAdminTerminalSession } from "@/composables/useAdminTerminalSession";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.titles.adminTerminal")));

const cfg = ref<AdminPublicConfig | null>(null);
const cfgError = ref<string>("");
const account = ref<string>("");
const workDir = ref<string>("");

// A fully interactive terminal: every keystroke is forwarded to the child's
// stdin, so the admin types commands directly.
const session = useAdminTerminalSession();
const { phase, statusMsg, lastError, fullscreen } = session;

// Every configured provider, of any type, can back a terminal.
const accounts = computed(() => cfg.value?.providers ?? []);

// First-party accounts get a "Login" button that runs their headless login
// flow. Not offered for the compatible types: those authenticate with an API
// key from the provider's env block, and the vendors' OAuth flows are
// meaningless against them.
const selectedCanLogin = computed(() => {
  const type = accounts.value.find((a) => a.name === account.value)?.type;
  return type === "claude" || type === "codex";
});

const overlayLabel = computed(() =>
  workDir.value ? `${account.value} · ${workDir.value}` : account.value,
);

onMounted(async () => {
  try {
    cfg.value = await adminFetchPublicConfig();
    if (accounts.value.length > 0) {
      account.value = accounts.value[0].name;
    }
    if (cfg.value?.default_home_root) {
      workDir.value = cfg.value.default_home_root;
    }
  } catch (e) {
    cfgError.value = errorMessage(e);
  }
});

function startTerminal(login = false) {
  session.start({ account: account.value, workDir: workDir.value, login });
}
</script>

<template>
  <div v-show="!fullscreen" class="mx-auto w-full max-w-[760px] px-4 pb-8">
    <SettingsDetailHeader
      :title="t('settings.titles.adminTerminal')"
      :eyebrow="t('settings.groups.admin')"
      parent-route-name="settings-admin"
    />
    <p class="text-[12px] text-muted-foreground mb-5">
      {{ t("settings.adminTerminal.intro") }}
    </p>

    <p v-if="cfgError" class="mb-3 text-[12px] text-red-600">{{ cfgError }}</p>

    <section class="space-y-4">
      <div class="border border-border rounded-md bg-card p-4 space-y-3">
        <div>
          <label class="block text-[12px] font-semibold mb-2">
            {{ t("settings.adminTerminal.account") }}
          </label>
          <select
            v-model="account"
            class="admin-terminal-account w-full border border-border rounded-md px-2 py-1.5 text-[13px] bg-background"
            :disabled="phase === 'running' || phase === 'connecting'"
          >
            <option v-for="a in accounts" :key="a.name" :value="a.name">
              {{ a.name }} ({{ a.type }})
            </option>
          </select>
          <p v-if="accounts.length === 0 && !cfgError" class="mt-2 text-[12px] text-amber-600">
            {{ t("settings.adminTerminal.noAccounts") }}
          </p>
        </div>
        <div>
          <label class="block text-[12px] font-semibold mb-2">
            {{ t("settings.adminTerminal.workDir") }}
          </label>
          <input
            v-model="workDir"
            type="text"
            autocomplete="off"
            spellcheck="false"
            class="admin-terminal-workdir w-full border border-border rounded-md px-2 py-1.5 text-[13px] bg-background font-mono"
            :placeholder="t('settings.adminTerminal.workDirPlaceholder')"
            :disabled="phase === 'running' || phase === 'connecting'"
          />
          <p class="mt-1 text-[12px] text-muted-foreground">
            {{ t("settings.adminTerminal.workDirHint") }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <Button
            class="admin-terminal-start"
            :disabled="!account || phase === 'connecting'"
            @click="startTerminal()"
          >
            {{
              phase === "connecting"
                ? t("settings.adminTerminal.connecting")
                : t("settings.adminTerminal.start")
            }}
          </Button>
          <Button
            v-if="selectedCanLogin"
            variant="outline"
            class="admin-terminal-login"
            :disabled="!account || phase === 'connecting'"
            @click="startTerminal(true)"
          >
            {{ t("settings.adminTerminal.login") }}
          </Button>
        </div>
        <p class="text-[12px] text-muted-foreground">
          {{ t("settings.adminTerminal.warning") }}
        </p>
      </div>

      <div
        v-if="statusMsg"
        class="text-[12px] px-3 py-2 rounded-md border border-border bg-accent/30"
      >
        {{ statusMsg }}
      </div>
      <div
        v-if="lastError"
        class="admin-terminal-error text-[12px] px-3 py-2 rounded-md border border-red-300 bg-red-50 text-red-700"
      >
        {{ lastError }}
      </div>
    </section>
  </div>

  <AdminTerminalOverlay :session="session" :label="overlayLabel" />
</template>
