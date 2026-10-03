<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Mark } from "@/components/ui/mark";
import { Dot } from "@/components/ui/dot";
import { Badge } from "@/components/ui/badge";
import LanguageSwitcher from "@/components/LanguageSwitcher.vue";
import PoweredBy from "@/components/PoweredBy.vue";
import { ArrowRight, Braces, CircleCheck, Clock3, FolderCode, ShieldCheck } from "lucide-vue-next";
import { safeRedirect } from "@/lib/safeRedirect";
import {
  login,
  fetchAuthOptions,
  fetchBootstrapStatus,
  type AuthOptions,
} from "@/composables/useApi";
import { errorMessage } from "@/lib/errorMessage";

// Build-time version surfaced as a small mono pill in the brand strip.
// Injected by vite.config.ts via the __APP_VERSION__ define (resolved from
// `git describe --tags --always --dirty` at build time, or the APP_VERSION
// env var when set).
const APP_VERSION = __APP_VERSION__;

const route = useRoute();
const router = useRouter();
const { t } = useI18n();

const username = ref("");
const password = ref("");
const error = ref("");
const submitting = ref(false);

// authOptions is fetched on mount; defaults assume password login is enabled
// and OIDC is off so the form renders something sensible during the request.
const authOptions = ref<AuthOptions>({
  password_login_enabled: true,
  oidc: { enabled: false, button_label: "" },
});

const passwordEnabled = computed(() => authOptions.value.password_login_enabled);
const oidcEnabled = computed(() => authOptions.value.oidc.enabled);
const oidcLabel = computed(
  () => authOptions.value.oidc.button_label || t("login.ssoFallbackLabel"),
);

const formRef = ref<HTMLFormElement | null>(null);

// A static preview of the workbench makes the product legible before login.
// Tone values map to Badge variants; no live account data is exposed here.
type AgentTone = "success" | "warning" | "secondary";
interface SampleTask {
  titleKey: string;
  statusKey: string;
  provider: string;
  tone: AgentTone;
}
const sampleTasks: SampleTask[] = [
  {
    titleKey: "login.workbench.tasks.backendTitle",
    statusKey: "login.workbench.tasks.running",
    provider: "Codex",
    tone: "success",
  },
  {
    titleKey: "login.workbench.tasks.frontendTitle",
    statusKey: "login.workbench.tasks.waiting",
    provider: "Claude Code",
    tone: "warning",
  },
  {
    titleKey: "login.workbench.tasks.reviewTitle",
    statusKey: "login.workbench.tasks.queued",
    provider: "Codex",
    tone: "secondary",
  },
];

onMounted(async () => {
  // First-run gate: a brand-new install has no login-capable users yet.
  // Bounce the visitor to /setup/admin before showing the password form
  // so they aren't staring at a login screen with nothing valid to type —
  // unless SSO provisions the first admin, in which case the SSO button is
  // the way in.
  try {
    const status = await fetchBootstrapStatus();
    if (status.setup_required) {
      router.replace({ name: "setup-admin" });
      return;
    }
  } catch {
    // Probe failure is non-fatal — fall through and render the form.
  }

  // Surface the OIDC error from the callback redirect (?oidc_error=...) so
  // failed SSO logins land back on /login with a visible reason instead of
  // silently looping.
  const oidcError = route.query.oidc_error;
  if (typeof oidcError === "string" && oidcError) {
    error.value = oidcError;
  }
  try {
    authOptions.value = await fetchAuthOptions();
  } catch {
    // Non-fatal: keep defaults so the password form still works even if
    // /api/auth/options is briefly unreachable.
  }
  if (passwordEnabled.value) {
    const input = formRef.value?.querySelector<HTMLInputElement>("input");
    input?.focus?.();
  }
});

function startOIDC() {
  // Top-level navigation, not XHR — the IdP needs the browser to follow
  // redirects through cookies, etc.
  window.location.assign("/api/auth/oidc/login");
}

async function onSubmit() {
  if (submitting.value) return;
  if (!username.value.trim() || !password.value) {
    error.value = t("login.errors.missingFields");
    return;
  }
  submitting.value = true;
  error.value = "";
  try {
    const user = await login(username.value.trim(), password.value);
    // ?redirect is attacker-controlled; only a same-origin path survives.
    const target = safeRedirect(route.query.redirect);
    // Hard reload so the SPA re-bootstraps users/conversations with the new session.
    window.location.assign(target === "/" && user.is_admin ? "/settings" : target);
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (status === 401) {
      error.value = t("login.errors.invalidCredentials");
    } else {
      error.value = errorMessage(e, t("login.errors.generic"));
    }
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="login-shell relative h-full w-full overflow-y-auto">
    <div
      aria-hidden="true"
      class="pointer-events-none absolute inset-0 opacity-70"
      :style="{
        backgroundImage:
          'radial-gradient(circle at 1px 1px, rgba(65, 70, 95, 0.1) 1px, transparent 0)',
        backgroundSize: '24px 24px',
        maskImage: 'linear-gradient(to bottom, black, transparent 68%)',
      }"
    />

    <div
      class="relative mx-auto flex min-h-full w-full max-w-[1440px] flex-col px-5 py-5 sm:px-8 sm:py-7 lg:px-12"
    >
      <header class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <Mark :size="44" />
          <div>
            <div class="text-[18px] font-extrabold tracking-[-0.045em]">DayMug</div>
            <div class="font-mono text-[10px] tracking-[0.08em] text-muted-foreground">
              {{ t("login.brandTagline") }} · {{ APP_VERSION }}
            </div>
          </div>
        </div>
        <LanguageSwitcher />
      </header>

      <main
        class="grid flex-1 items-center gap-12 py-12 lg:grid-cols-[minmax(0,1fr)_minmax(360px,430px)] lg:gap-16 lg:py-16 xl:gap-24"
      >
        <section class="min-w-0">
          <div
            class="mb-6 inline-flex items-center gap-2 rounded-full border border-[#5b5ce2]/20 bg-white/75 px-3 py-1.5 text-[11px] font-semibold text-[#4748bd] shadow-sm backdrop-blur"
          >
            <Dot tone="connected" :size="7" />
            {{ t("login.cliOnline") }}
          </div>

          <h1
            class="max-w-[880px] text-[clamp(2.85rem,5.3vw,5.4rem)] font-extrabold leading-[1.02] tracking-[-0.055em] text-[#171923]"
          >
            <span class="block">{{ t("login.headlineLine1") }}</span>
            <span class="block text-[#5b5ce2]">{{ t("login.headlineLine2") }}</span>
          </h1>

          <p class="mt-6 max-w-2xl text-[15px] leading-7 text-[#5f6472] sm:text-[16px]">
            {{ t("login.description") }}
          </p>

          <div class="mt-8 hidden max-w-2xl gap-3 sm:grid sm:grid-cols-3">
            <div class="feature-chip">
              <Braces class="size-4 text-[#5b5ce2]" />
              <div>
                <div class="feature-chip-title">{{ t("login.features.agentsTitle") }}</div>
                <div class="feature-chip-copy">{{ t("login.features.agentsCopy") }}</div>
              </div>
            </div>
            <div class="feature-chip">
              <Clock3 class="size-4 text-[#5b5ce2]" />
              <div>
                <div class="feature-chip-title">{{ t("login.features.sessionsTitle") }}</div>
                <div class="feature-chip-copy">{{ t("login.features.sessionsCopy") }}</div>
              </div>
            </div>
            <div class="feature-chip">
              <FolderCode class="size-4 text-[#5b5ce2]" />
              <div>
                <div class="feature-chip-title">{{ t("login.features.localTitle") }}</div>
                <div class="feature-chip-copy">{{ t("login.features.localCopy") }}</div>
              </div>
            </div>
          </div>

          <Card
            data-testid="workbench-preview"
            class="workbench-card mt-9 hidden max-w-2xl gap-0 overflow-hidden border-[#dfe2ec] bg-white/75 py-0 shadow-[0_16px_50px_rgba(45,49,76,0.08)] backdrop-blur sm:block"
          >
            <CardContent class="p-0">
              <div class="flex items-center justify-between border-b border-[#e6e8f0] px-5 py-4">
                <div>
                  <div class="text-[13px] font-bold text-[#232633]">
                    {{ t("login.workbench.title") }}
                  </div>
                  <div class="mt-0.5 text-[11px] text-[#7a7f8d]">
                    {{ t("login.workbench.subtitle") }}
                  </div>
                </div>
                <Badge
                  variant="secondary"
                  class="border border-[#5b5ce2]/15 bg-[#eeefff] text-[#4c4db8]"
                >
                  {{ t("login.workbench.taskCount", { n: sampleTasks.length }) }}
                </Badge>
              </div>
              <ul class="grid divide-y divide-[#eceef4] lg:grid-cols-3 lg:divide-x lg:divide-y-0">
                <li v-for="task in sampleTasks" :key="task.titleKey" class="px-5 py-4">
                  <div class="flex items-center justify-between gap-3">
                    <span class="font-mono text-[10px] text-[#8b8f9c]">{{ task.provider }}</span>
                    <Badge :variant="task.tone" class="px-2 py-0 text-[10px]">
                      {{ t(task.statusKey) }}
                    </Badge>
                  </div>
                  <div class="mt-3 text-[13px] font-semibold leading-5 text-[#2b2e3a]">
                    {{ t(task.titleKey) }}
                  </div>
                </li>
              </ul>
            </CardContent>
          </Card>
        </section>

        <Card
          class="auth-card gap-0 border-white/90 bg-white/90 py-0 shadow-[0_28px_80px_rgba(48,52,82,0.16)] backdrop-blur-xl"
        >
          <CardContent class="p-6 sm:p-8">
            <div class="mb-7">
              <div
                class="mb-5 flex size-11 items-center justify-center rounded-2xl bg-[#eeefff] text-[#5556c9]"
              >
                <CircleCheck class="size-5" />
              </div>
              <h2 class="text-[24px] font-extrabold tracking-[-0.035em] text-[#1e2029]">
                {{ t("login.cardTitle") }}
              </h2>
              <p class="mt-2 text-[13px] leading-5 text-[#707583]">
                {{ t("login.cardDescription") }}
              </p>
            </div>

            <form v-if="passwordEnabled" ref="formRef" class="space-y-5" @submit.prevent="onSubmit">
              <div>
                <label for="login-username" class="login-label">{{ t("login.username") }}</label>
                <Input
                  id="login-username"
                  v-model="username"
                  type="text"
                  autocomplete="username"
                  class="h-12 border-[#dde0e8] bg-[#f8f9fc] px-4 shadow-none focus-visible:border-[#5b5ce2] focus-visible:ring-[#5b5ce2]/15"
                  :disabled="submitting"
                />
              </div>

              <div>
                <label for="login-password" class="login-label">{{ t("login.password") }}</label>
                <Input
                  id="login-password"
                  v-model="password"
                  type="password"
                  autocomplete="current-password"
                  class="h-12 border-[#dde0e8] bg-[#f8f9fc] px-4 shadow-none focus-visible:border-[#5b5ce2] focus-visible:ring-[#5b5ce2]/15"
                  :disabled="submitting"
                />
              </div>

              <div
                v-if="error"
                role="alert"
                class="rounded-xl border border-destructive/25 bg-destructive/10 px-3.5 py-3 text-sm text-destructive"
              >
                {{ error }}
              </div>

              <Button
                type="submit"
                size="lg"
                class="h-12 w-full gap-2 rounded-xl bg-[#252733] text-white shadow-[0_8px_20px_rgba(37,39,51,0.18)] hover:bg-[#5b5ce2]"
                :disabled="submitting"
              >
                {{ submitting ? t("login.submitting") : t("login.submit") }}
                <ArrowRight class="size-4" />
              </Button>
            </form>

            <div
              v-if="!passwordEnabled && error"
              role="alert"
              class="rounded-xl border border-destructive/25 bg-destructive/10 px-3.5 py-3 text-sm text-destructive"
            >
              {{ error }}
            </div>

            <div v-if="oidcEnabled" :class="passwordEnabled ? 'mt-6' : ''" class="space-y-4">
              <div v-if="passwordEnabled" class="flex items-center gap-3">
                <span class="h-px flex-1 bg-[#e2e4eb]" />
                <span class="text-[11px] text-[#969aa5]">
                  {{ t("login.or") }}
                </span>
                <span class="h-px flex-1 bg-[#e2e4eb]" />
              </div>
              <Button
                type="button"
                variant="outline"
                size="lg"
                class="h-12 w-full gap-2 rounded-xl border-[#d9dce5] bg-white"
                @click="startOIDC"
              >
                {{ oidcLabel }}
                <ArrowRight class="size-4" />
              </Button>
            </div>

            <div class="mt-7 flex items-start gap-2.5 border-t border-[#eceef3] pt-5">
              <ShieldCheck class="mt-0.5 size-4 shrink-0 text-[#5b5ce2]" />
              <p class="text-[11px] leading-5 text-[#858a97]">
                {{ t("login.sessionsHint") }}
              </p>
            </div>
          </CardContent>
        </Card>
      </main>

      <footer
        class="flex flex-wrap items-center justify-between gap-3 border-t border-[#dfe2ea] py-5 text-[11px] text-[#7c818e]"
      >
        <span>{{ t("login.footerLocal") }}</span>
        <span class="flex items-center gap-3">
          <PoweredBy />
          <span class="font-mono">{{ APP_VERSION }}</span>
        </span>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.login-shell {
  background:
    radial-gradient(circle at 76% 18%, rgba(91, 92, 226, 0.13), transparent 29rem),
    linear-gradient(145deg, #f8f9fd 0%, #f2f4fa 52%, #eef0f7 100%);
}

.feature-chip {
  display: flex;
  min-width: 0;
  gap: 0.65rem;
  border-left: 1px solid #d9dce7;
  padding-left: 0.9rem;
}

.feature-chip-title {
  color: #2f3240;
  font-size: 0.75rem;
  font-weight: 700;
  line-height: 1.1rem;
}

.feature-chip-copy {
  margin-top: 0.125rem;
  color: #828694;
  font-size: 0.625rem;
  line-height: 1rem;
}

.login-label {
  margin-bottom: 0.5rem;
  display: block;
  color: #565b69;
  font-size: 0.75rem;
  font-weight: 700;
}

@media (prefers-reduced-motion: no-preference) {
  .workbench-card {
    animation: login-rise 600ms 120ms both cubic-bezier(0.2, 0.8, 0.2, 1);
  }

  .auth-card {
    animation: login-rise 600ms 220ms both cubic-bezier(0.2, 0.8, 0.2, 1);
  }
}

@keyframes login-rise {
  from {
    opacity: 0;
    transform: translateY(12px);
  }

  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
