<script setup lang="ts">
import { ref, onMounted, computed, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Mark } from "@/components/ui/mark";
import { Dot } from "@/components/ui/dot";
import LanguageSwitcher from "@/components/LanguageSwitcher.vue";
import { ArrowRight } from "lucide-vue-next";
import { fetchBootstrapStatus, createBootstrapAdmin, type SetupMode } from "@/composables/useApi";
import { errorMessage } from "@/lib/errorMessage";

const APP_VERSION = __APP_VERSION__;

// Where a fresh admin lands: configuring an AI provider is the one step
// without which nothing else in the app works.
const AFTER_SETUP_PATH = "/settings/admin/models";

// Same shape the server enforces (service.ValidateUsername): the username
// names the home directory, so it must be one plain path segment.
const USERNAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

const router = useRouter();
const { t } = useI18n();

const email = ref("");
const username = ref("");
const password = ref("");
const passwordConfirm = ref("");
const error = ref("");
const submitting = ref(false);
// Default to invisible until we've confirmed the install is genuinely
// pre-bootstrap. Anyone who navigates here on an already-set-up install
// gets bounced to /login on mount, so flashing the form first would be a
// visual lie.
const ready = ref(false);
// Which form variant the server's sign-in config calls for. Defaults to the
// password form when the probe fails; the server re-checks on submit.
const mode = ref<SetupMode>("password");
const minPasswordLength = ref(8);
const needsPassword = computed(() => mode.value === "password");

// usernameLocal mirrors the typed email's local-part. We keep an
// explicit username field for visibility, but auto-fill it as the user
// types the email so the username==email-prefix invariant is satisfied
// without the visitor having to think about it. Manual edits to the
// username field are preserved.
const usernameTouched = ref(false);
const usernameLocal = computed(() => {
  const at = email.value.indexOf("@");
  return at > 0 ? email.value.slice(0, at) : "";
});

watch(email, () => {
  if (!usernameTouched.value) {
    username.value = usernameLocal.value;
  }
});

function onUsernameInput() {
  usernameTouched.value = true;
}

onMounted(async () => {
  try {
    const status = await fetchBootstrapStatus();
    if (!status.setup_required) {
      // First-run setup is done, or SSO provisions the first admin — fall
      // back to the normal login flow.
      router.replace({ name: "login" });
      return;
    }
    if (status.setup_mode) mode.value = status.setup_mode;
    if (status.min_password_length) minPasswordLength.value = status.min_password_length;
    ready.value = true;
  } catch {
    // If the probe fails outright, show the form anyway. The server-side
    // check on submit is authoritative; a transient probe failure
    // shouldn't strand the operator on a blank page.
    ready.value = true;
  }
});

// validate returns the first client-side problem with the form, or "".
function validate(): string {
  const user = username.value.trim();
  if (!email.value.trim() || !user || (needsPassword.value && !password.value)) {
    return t(needsPassword.value ? "setup.errors.missingFields" : "setup.errors.missingFieldsSSO");
  }
  if (user !== usernameLocal.value) return t("setup.errors.usernameMismatch");
  if (!USERNAME_PATTERN.test(user)) return t("setup.errors.usernameInvalid");
  if (needsPassword.value) {
    if ([...password.value].length < minPasswordLength.value) {
      return t("setup.errors.passwordTooShort", { n: minPasswordLength.value });
    }
    if (password.value !== passwordConfirm.value) return t("setup.errors.passwordMismatch");
  }
  return "";
}

async function onSubmit() {
  if (submitting.value) return;
  const problem = validate();
  if (problem) {
    error.value = problem;
    return;
  }
  submitting.value = true;
  error.value = "";
  try {
    await createBootstrapAdmin({
      username: username.value.trim(),
      email: email.value.trim(),
      ...(needsPassword.value ? { password: password.value } : {}),
    });
    // Hard reload so the SPA re-bootstraps with the new session cookie
    // — mirrors what /login does on success.
    window.location.assign(AFTER_SETUP_PATH);
  } catch (e) {
    error.value = errorMessage(e, t("setup.errors.generic"));
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="relative grid min-h-full w-full bg-background lg:grid-cols-[1.1fr_0.9fr]">
    <div class="absolute right-6 top-6 sm:right-8 sm:top-8 z-20">
      <LanguageSwitcher />
    </div>

    <div class="relative flex flex-col justify-between p-8 sm:p-14 min-h-full">
      <div class="flex items-center gap-2.5">
        <Mark :size="22" />
        <span class="text-[20px] font-semibold tracking-tight">DayMug</span>
        <span class="ml-1.5 text-[11px] font-mono text-muted-foreground tracking-wide">{{
          APP_VERSION
        }}</span>
      </div>

      <div v-if="ready" class="max-w-[460px]">
        <h1
          class="text-5xl sm:text-6xl xl:text-[72px] font-semibold leading-[0.95] tracking-[-0.04em] text-foreground"
        >
          {{ t("setup.headlineLine1") }}<br />
          <span class="italic text-primary">{{ t("setup.headlineLine2") }}</span>
        </h1>
        <p class="mt-6 text-[15px] leading-[1.55] text-[var(--ink-2)] max-w-[420px]">
          {{ t("setup.description") }}
        </p>

        <div
          v-if="mode === 'unavailable'"
          role="alert"
          data-testid="setup-unavailable"
          class="mt-9 max-w-[420px] rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
        >
          <p class="font-semibold">{{ t("setup.unavailable.title") }}</p>
          <p class="mt-1.5 leading-[1.55]">{{ t("setup.unavailable.body") }}</p>
        </div>

        <form v-else class="mt-9 max-w-[380px] space-y-4" @submit.prevent="onSubmit">
          <p
            v-if="mode === 'sso_email'"
            data-testid="setup-sso-hint"
            class="rounded-md border border-border bg-[var(--paper-alt)] px-3 py-2 text-[13px] leading-[1.5] text-[var(--ink-2)]"
          >
            {{ t("setup.ssoEmailHint") }}
          </p>

          <div>
            <label
              for="setup-email"
              class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
              >{{ t("setup.email") }}</label
            >
            <Input
              id="setup-email"
              v-model="email"
              type="email"
              autocomplete="email"
              :disabled="submitting"
            />
          </div>

          <div>
            <label
              for="setup-username"
              class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
              >{{ t("setup.username") }}</label
            >
            <Input
              id="setup-username"
              v-model="username"
              type="text"
              autocomplete="username"
              :disabled="submitting"
              @input="onUsernameInput"
            />
            <p class="mt-1.5 text-[11px] text-muted-foreground">
              {{ t("setup.usernameHint") }}
            </p>
          </div>

          <template v-if="needsPassword">
            <div>
              <label
                for="setup-password"
                class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
                >{{ t("setup.password") }}</label
              >
              <Input
                id="setup-password"
                v-model="password"
                type="password"
                autocomplete="new-password"
                :disabled="submitting"
              />
              <p class="mt-1.5 text-[11px] text-muted-foreground">
                {{ t("setup.passwordHint", { n: minPasswordLength }) }}
              </p>
            </div>

            <div>
              <label
                for="setup-password-confirm"
                class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
                >{{ t("setup.passwordConfirm") }}</label
              >
              <Input
                id="setup-password-confirm"
                v-model="passwordConfirm"
                type="password"
                autocomplete="new-password"
                :disabled="submitting"
              />
            </div>
          </template>

          <div
            v-if="error"
            role="alert"
            class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {{ error }}
          </div>

          <div class="flex items-center gap-3 pt-1">
            <Button type="submit" size="lg" class="gap-2" :disabled="submitting">
              {{ submitting ? t("setup.submitting") : t("setup.submit") }}
              <ArrowRight class="size-4" />
            </Button>
          </div>
        </form>
      </div>

      <div class="flex items-center gap-3 text-xs text-muted-foreground">
        <Dot tone="connected" :size="8" />
        <span>{{ t("setup.firstRun") }}</span>
      </div>
    </div>

    <div
      class="relative hidden lg:flex flex-col justify-center p-14 overflow-hidden bg-[var(--paper-alt)] border-l border-border"
    >
      <div
        aria-hidden="true"
        class="pointer-events-none absolute inset-0 opacity-50"
        :style="{
          backgroundImage: 'radial-gradient(circle at 1px 1px, var(--border) 1px, transparent 0)',
          backgroundSize: '14px 14px',
        }"
      />
      <div class="relative z-10 max-w-[380px]">
        <div class="italic text-[22px] leading-[1.4] text-[var(--ink-2)] whitespace-pre-line">
          {{ t("setup.quote") }}
        </div>
        <div class="mt-3 text-xs font-mono text-muted-foreground">
          {{ t("setup.quoteAttribution") }}
        </div>
      </div>
    </div>
  </div>
</template>
