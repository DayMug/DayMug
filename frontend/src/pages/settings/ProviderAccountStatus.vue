<script setup lang="ts">
// Per-account "is this usable?" strip on the models page: check the CLI's
// login state plus one real turn, and log the account in from right here, so
// configuring, authenticating and verifying an account all happen on one page.
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { CircleCheck, CircleX, LoaderCircle, LogIn, Stethoscope } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import { adminCheckAccount, type AdminAccountCheck } from "@/composables/useApi";
import { useAdminTerminalSession } from "@/composables/useAdminTerminalSession";
import AdminTerminalOverlay from "./AdminTerminalOverlay.vue";
import { errorMessage } from "@/lib/errorMessage";

const props = defineProps<{
  account: string;
  type: string;
  // Unsaved account edits: the server doesn't know this account (or its new
  // credentials) yet, so neither checking nor logging in would mean anything.
  disabled?: boolean;
}>();

const { t } = useI18n();
const checking = ref(false);
const result = ref<AdminAccountCheck | null>(null);
const error = ref("");

// The compatible types authenticate with an API key in the env block.
const canLogin = computed(() => props.type === "claude" || props.type === "codex");

const session = useAdminTerminalSession();

async function check() {
  if (checking.value || props.disabled) return;
  checking.value = true;
  error.value = "";
  try {
    result.value = await adminCheckAccount(props.account);
  } catch (e) {
    result.value = null;
    error.value = errorMessage(e);
  } finally {
    checking.value = false;
  }
}

function login() {
  if (props.disabled) return;
  session.start({ account: props.account, login: true });
}

// Closing the login terminal is when the admin expects to see whether it
// worked, so re-check right away instead of making them click again.
function onTerminalClosed() {
  void check();
}

// A result describes one account at one moment; a rename or type change makes
// it describe something else.
watch(
  () => [props.account, props.type],
  () => {
    result.value = null;
    error.value = "";
  },
);

const authLabel = computed(() => {
  const auth = result.value?.auth;
  if (!auth) return "";
  if (!auth.checked) return t("settings.adminModels.check.authEnv");
  if (auth.error) return t("settings.adminModels.check.authError", { error: auth.error });
  if (!auth.logged_in) return t("settings.adminModels.check.loggedOut");
  const how = [auth.method, auth.detail].filter(Boolean).join(" · ");
  return how
    ? t("settings.adminModels.check.loggedInAs", { how })
    : t("settings.adminModels.check.loggedIn");
});

const runLabel = computed(() => {
  const run = result.value?.run;
  if (!run || !run.attempted) return "";
  const model = run.model || t("settings.adminModels.check.defaultModel");
  if (run.ok) {
    return t("settings.adminModels.check.runOK", {
      model,
      seconds: ((run.latency_ms ?? 0) / 1000).toFixed(1),
    });
  }
  return t("settings.adminModels.check.runFailed", { model, error: run.error ?? "" });
});
</script>

<template>
  <section class="mt-4 border-t border-border/70 pt-4" :data-testid="`account-status-${account}`">
    <div class="flex flex-wrap items-center gap-2">
      <h3 class="mr-auto text-[13px] font-semibold">
        {{ t("settings.adminModels.check.title") }}
      </h3>
      <Button
        size="sm"
        variant="outline"
        class="gap-1.5"
        :disabled="disabled || checking"
        data-testid="account-check"
        @click="check"
      >
        <LoaderCircle v-if="checking" class="size-3.5 animate-spin" />
        <Stethoscope v-else class="size-3.5" />
        {{
          checking ? t("settings.adminModels.check.checking") : t("settings.adminModels.check.run")
        }}
      </Button>
      <Button
        v-if="canLogin"
        size="sm"
        variant="outline"
        class="gap-1.5"
        :disabled="disabled || session.phase.value === 'connecting'"
        data-testid="account-login"
        @click="login"
      >
        <LogIn class="size-3.5" />
        {{ t("settings.adminModels.check.login") }}
      </Button>
    </div>

    <p
      v-if="disabled"
      class="mt-2 text-[11px] text-muted-foreground"
      data-testid="account-status-unsaved"
    >
      {{ t("settings.adminModels.check.saveFirst") }}
    </p>
    <p v-else-if="!result && !error && !checking" class="mt-2 text-[11px] text-muted-foreground">
      {{
        canLogin ? t("settings.adminModels.check.hint") : t("settings.adminModels.check.hintEnv")
      }}
    </p>
    <p v-if="checking" class="mt-2 text-[11px] text-muted-foreground">
      {{ t("settings.adminModels.check.checkingHint") }}
    </p>

    <div
      v-if="result && !checking"
      class="mt-2 space-y-1 rounded-md border px-3 py-2 text-[12px]"
      :class="result.ok ? 'border-emerald-500/40' : 'border-destructive/40'"
      data-testid="account-check-result"
    >
      <p class="flex items-center gap-1.5 font-medium">
        <CircleCheck v-if="result.ok" class="size-4 text-emerald-600" />
        <CircleX v-else class="size-4 text-destructive" />
        {{
          result.ok ? t("settings.adminModels.check.ok") : t("settings.adminModels.check.failed")
        }}
        <span class="font-normal text-muted-foreground">· {{ result.transport }}</span>
      </p>
      <p class="break-words text-muted-foreground" data-testid="account-check-auth">
        {{ authLabel }}
      </p>
      <p v-if="runLabel" class="break-words text-muted-foreground" data-testid="account-check-run">
        {{ runLabel }}
      </p>
    </div>
    <p v-if="error" class="mt-2 text-[12px] text-red-600">{{ error }}</p>
    <p v-if="session.lastError.value" class="mt-2 text-[12px] text-red-600">
      {{ session.lastError.value }}
    </p>

    <AdminTerminalOverlay
      :session="session"
      :label="t('settings.adminModels.check.loginTerminal', { account })"
      @close="onTerminalClosed"
    />
  </section>
</template>
