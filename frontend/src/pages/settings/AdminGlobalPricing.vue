<script setup lang="ts">
// Admin-editable USD-per-million-token table. One provider tab at a time.
//
// Self-contained: it fetches its own rates on mount and after every provider
// switch, so no pricing state leaks into the parent page. The model list comes
// from the server's pricing catalog rather than being hard-coded here.
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminFetchPricing, adminSavePricing } from "@/composables/useApi";
import type { AdminPricing, AdminPricingRate } from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { Button } from "@/components/ui/button";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();

// The providers whose per-turn cost DayMug computes locally. `claude` is
// absent on purpose: Claude Code reports a total_cost_usd that already
// matches Anthropic's bill, so there is nothing for an admin to correct.
const pricingProviders = ["codex", "openai-compatible", "claude-compatible"] as const;
type PricingProvider = (typeof pricingProviders)[number];
const pricingProvider = ref<PricingProvider>("codex");
const pricing = ref<AdminPricing | null>(null);

type PricingDraftRow = {
  input: string;
  cachedInput: string;
  output: string;
  cacheCreation5m: string;
  cacheCreation1h: string;
};
const pricingDraft = ref<Record<string, PricingDraftRow>>({});
const {
  busy: pricingSaving,
  error: pricingErr,
  message: pricingMsg,
  run: runPricing,
  reset: resetPricingStatus,
} = useAsyncOperation();

// providerUsesCacheWrites is the single source of truth for which columns the
// template renders and which fields onSavePricing forwards. The Codex family
// doesn't use the 5m / 1h cache-creation fields (OpenAI doesn't split cache
// writes by TTL); an Anthropic-compatible endpoint bills all five.
function providerUsesCacheWrites(p: string): boolean {
  return p === "claude-compatible";
}

// The provider the *loaded rates* belong to, straight off the payload.
//
// Every read of the table — which columns to draw, which provider to save
// back to — goes through this rather than through `pricingProvider`, which
// only tracks which tab is highlighted. The two diverge whenever a switch is
// in flight, and binding the save to the tab is what let one provider's model
// keys get written into another's rate table.
const loadedProvider = computed(() => pricing.value?.provider ?? "");

// parseNum returns the numeric value of a draft cell, treating blanks as 0 so
// "no override" survives a round-trip without flagging the form dirty.
function parseNum(s: string): number {
  return s === "" ? 0 : parseFloat(s);
}

const pricingDirty = computed(() => {
  if (!pricing.value) return false;
  const useCache = providerUsesCacheWrites(loadedProvider.value);
  for (const m of pricing.value.models) {
    const d = pricingDraft.value[m];
    const r = pricing.value.rates[m];
    if (!d || !r) return true;
    // Backend treats missing/0 optional fields as "no override". Mirror that
    // so an empty cell + a 0 server value aren't flagged dirty.
    if (parseNum(d.input) !== r.input) return true;
    if (parseNum(d.output) !== r.output) return true;
    if (parseNum(d.cachedInput) !== (r.cached_input ?? 0)) return true;
    if (useCache) {
      if (parseNum(d.cacheCreation5m) !== (r.cache_creation_5m ?? 0)) return true;
      if (parseNum(d.cacheCreation1h) !== (r.cache_creation_1h ?? 0)) return true;
    }
  }
  return false;
});

// hydrateDraft seeds the editable form from a server response. Called on first
// load, on provider switch, and after each save so the dirty check resets
// without a parent re-mount. Optional fields render as blanks so the
// placeholder ("auto / no override") drives the UX — storing "0" would falsely
// suggest the admin chose a zero rate.
function hydrateDraft(p: AdminPricing) {
  const draft: Record<string, PricingDraftRow> = {};
  for (const m of p.models) {
    const r = p.rates[m] ?? p.defaults[m] ?? { input: 0, output: 0 };
    draft[m] = {
      input: String(r.input ?? 0),
      cachedInput: r.cached_input ? String(r.cached_input) : "",
      output: String(r.output ?? 0),
      cacheCreation5m: r.cache_creation_5m ? String(r.cache_creation_5m) : "",
      cacheCreation1h: r.cache_creation_1h ? String(r.cache_creation_1h) : "",
    };
  }
  pricingDraft.value = draft;
}

// Tabs stay clickable during a load (the fetch is quick and disabling them
// makes the switch feel stuck), so two loads can overlap. `pricingLoadRun`
// makes the newest click the winner regardless of which response lands
// first — same guard as WorkspacePreview's probe. It is checked before every
// state write, including the error line: a superseded failure must not
// replace the status of the request the admin is actually waiting on.
let pricingLoadRun = 0;
const pricingLoading = ref(false);

async function loadPricing(provider: PricingProvider) {
  const run = ++pricingLoadRun;
  // Not routed through runPricing: `run()` has no notion of generations, and
  // its busy ref is the saving flag, which must stay false here.
  resetPricingStatus();
  pricingLoading.value = true;
  try {
    const p = await adminFetchPricing(provider);
    if (run !== pricingLoadRun) return;
    pricing.value = p;
    hydrateDraft(p);
  } catch (e) {
    if (run !== pricingLoadRun) return;
    pricing.value = null;
    pricingErr.value = errorMessage(e);
  } finally {
    if (run === pricingLoadRun) pricingLoading.value = false;
  }
}

async function onChangePricingProvider(next: PricingProvider) {
  if (next === pricingProvider.value) return;
  pricingProvider.value = next;
  // Drop the outgoing provider's rows immediately. Leaving them on screen
  // under the newly highlighted tab invites the admin to edit figures that
  // belong to the provider they just navigated away from.
  pricing.value = null;
  pricingDraft.value = {};
  await loadPricing(next);
}

onMounted(() => loadPricing(pricingProvider.value));

async function onSavePricing() {
  if (!pricing.value) return;
  // Clear stale status up front so a validation failure below doesn't leave a
  // previous run's success message standing next to the error.
  resetPricingStatus();
  // Save back to the provider these rates were loaded from — never to the
  // highlighted tab. The models being iterated below come from this same
  // payload, so provider and model keys cannot disagree.
  const target = pricing.value.provider;
  const useCache = providerUsesCacheWrites(target);
  // Build the override map: every model gets sent (no partial saves) so the
  // server doesn't have to reason about which entries are missing vs. cleared.
  // Empty/NaN strings collapse to 0 — the negative-rate check below catches
  // typos before we round-trip.
  const rates: Record<string, AdminPricingRate> = {};
  for (const m of pricing.value.models) {
    const d = pricingDraft.value[m];
    if (!d) continue;
    const inN = parseFloat(d.input);
    const outN = parseFloat(d.output);
    const cachedN = parseNum(d.cachedInput);
    const c5mN = useCache ? parseNum(d.cacheCreation5m) : 0;
    const c1hN = useCache ? parseNum(d.cacheCreation1h) : 0;
    if (
      Number.isNaN(inN) ||
      Number.isNaN(outN) ||
      Number.isNaN(cachedN) ||
      Number.isNaN(c5mN) ||
      Number.isNaN(c1hN) ||
      inN < 0 ||
      outN < 0 ||
      cachedN < 0 ||
      c5mN < 0 ||
      c1hN < 0
    ) {
      pricingErr.value = t("settings.adminGlobal.pricingNegativeError");
      return;
    }
    const rate: AdminPricingRate = { input: inN, output: outN };
    // Omit optional fields from the JSON when blank/zero, matching the
    // omitempty JSON tag on the server struct so a fresh GET round-trips back
    // to the same draft state.
    if (cachedN > 0) rate.cached_input = cachedN;
    if (c5mN > 0) rate.cache_creation_5m = c5mN;
    if (c1hN > 0) rate.cache_creation_1h = c1hN;
    rates[m] = rate;
  }
  await runPricing(async () => {
    const updated = await adminSavePricing(target, rates);
    pricing.value = updated;
    hydrateDraft(updated);
    pricingMsg.value = t("settings.adminGlobal.pricingSaved");
  });
}

async function onResetPricing() {
  if (!pricing.value) return;
  const target = pricing.value.provider;
  await runPricing(async () => {
    const updated = await adminSavePricing(target, {});
    pricing.value = updated;
    hydrateDraft(updated);
    pricingMsg.value = t("settings.adminGlobal.pricingReset");
  });
}
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4" data-testid="provider-pricing">
    <h2 class="text-[14px] font-semibold mb-2">
      {{ t("settings.adminGlobal.pricingTitle") }}
    </h2>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.pricingIntro") }}
    </p>
    <div class="mb-3 inline-flex rounded-md border border-border overflow-hidden">
      <button
        v-for="p in pricingProviders"
        :key="p"
        type="button"
        class="px-3 py-1 text-[12px] font-mono transition-colors"
        :class="
          p === pricingProvider
            ? 'bg-accent text-accent-foreground'
            : 'bg-background hover:bg-accent/50 text-muted-foreground'
        "
        :disabled="pricingSaving"
        @click="onChangePricingProvider(p)"
      >
        {{ p }}
      </button>
    </div>
    <p
      v-if="pricingLoading"
      class="text-[12px] text-muted-foreground"
      data-testid="pricing-loading"
    >
      {{ t("common.loading") }}
    </p>
    <div v-if="pricing && pricing.models.length">
      <table class="w-full text-[13px]">
        <thead class="text-muted-foreground text-[11px] uppercase">
          <tr>
            <th class="text-left py-1">{{ t("settings.adminGlobal.pricingModel") }}</th>
            <th class="text-right py-1">{{ t("settings.adminGlobal.pricingInput") }}</th>
            <th class="text-right py-1" :title="t('settings.adminGlobal.pricingCachedInputHint')">
              {{ t("settings.adminGlobal.pricingCachedInput") }}
            </th>
            <th
              v-if="providerUsesCacheWrites(loadedProvider)"
              class="text-right py-1"
              :title="t('settings.adminGlobal.pricingCacheCreation5mHint')"
            >
              {{ t("settings.adminGlobal.pricingCacheCreation5m") }}
            </th>
            <th
              v-if="providerUsesCacheWrites(loadedProvider)"
              class="text-right py-1"
              :title="t('settings.adminGlobal.pricingCacheCreation1hHint')"
            >
              {{ t("settings.adminGlobal.pricingCacheCreation1h") }}
            </th>
            <th class="text-right py-1">{{ t("settings.adminGlobal.pricingOutput") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in pricing.models" :key="m" class="border-t border-border">
            <td class="py-1.5 font-mono align-top">
              {{ m }}
              <p v-if="pricing.defaults[m]" class="mt-0.5 text-[10px] text-muted-foreground">
                {{
                  providerUsesCacheWrites(loadedProvider)
                    ? t("settings.adminGlobal.pricingDefaultHintFull", {
                        input: pricing.defaults[m].input,
                        cachedInput: pricing.defaults[m].cached_input ?? 0,
                        cacheCreation5m: pricing.defaults[m].cache_creation_5m ?? 0,
                        cacheCreation1h: pricing.defaults[m].cache_creation_1h ?? 0,
                        output: pricing.defaults[m].output,
                      })
                    : t("settings.adminGlobal.pricingDefaultHint", {
                        input: pricing.defaults[m].input,
                        cachedInput:
                          pricing.defaults[m].cached_input ?? pricing.defaults[m].input / 2,
                        output: pricing.defaults[m].output,
                      })
                }}
              </p>
            </td>
            <td class="py-1.5 text-right">
              <input
                v-if="pricingDraft[m]"
                v-model="pricingDraft[m].input"
                type="number"
                step="0.01"
                min="0"
                class="w-24 text-right font-mono text-[12px] border border-border rounded-md bg-background px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </td>
            <td class="py-1.5 text-right">
              <input
                v-if="pricingDraft[m]"
                v-model="pricingDraft[m].cachedInput"
                type="number"
                step="0.01"
                min="0"
                :placeholder="
                  providerUsesCacheWrites(loadedProvider)
                    ? '0'
                    : String((pricing.defaults[m]?.input ?? 0) / 2)
                "
                :title="t('settings.adminGlobal.pricingCachedInputHint')"
                class="w-24 text-right font-mono text-[12px] border border-border rounded-md bg-background px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </td>
            <td v-if="providerUsesCacheWrites(loadedProvider)" class="py-1.5 text-right">
              <input
                v-if="pricingDraft[m]"
                v-model="pricingDraft[m].cacheCreation5m"
                type="number"
                step="0.01"
                min="0"
                placeholder="0"
                :title="t('settings.adminGlobal.pricingCacheCreation5mHint')"
                class="w-24 text-right font-mono text-[12px] border border-border rounded-md bg-background px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </td>
            <td v-if="providerUsesCacheWrites(loadedProvider)" class="py-1.5 text-right">
              <input
                v-if="pricingDraft[m]"
                v-model="pricingDraft[m].cacheCreation1h"
                type="number"
                step="0.01"
                min="0"
                placeholder="0"
                :title="t('settings.adminGlobal.pricingCacheCreation1hHint')"
                class="w-24 text-right font-mono text-[12px] border border-border rounded-md bg-background px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </td>
            <td class="py-1.5 text-right">
              <input
                v-if="pricingDraft[m]"
                v-model="pricingDraft[m].output"
                type="number"
                step="0.01"
                min="0"
                class="w-24 text-right font-mono text-[12px] border border-border rounded-md bg-background px-2 py-1 focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </td>
          </tr>
        </tbody>
      </table>
      <div class="mt-3 flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          :disabled="pricingSaving || !pricingDirty"
          @click="onSavePricing"
        >
          {{ pricingSaving ? t("common.saving") : t("common.save") }}
        </Button>
        <Button variant="ghost" size="sm" :disabled="pricingSaving" @click="onResetPricing">
          {{ t("settings.adminGlobal.pricingResetDefaults") }}
        </Button>
        <span v-if="pricingDirty && !pricingSaving" class="text-[11px] text-muted-foreground">
          {{ t("settings.adminGlobal.unsavedChanges") }}
        </span>
      </div>
    </div>
    <p v-if="pricingMsg" class="mt-2 text-[12px] text-muted-foreground">{{ pricingMsg }}</p>
    <p v-if="pricingErr" class="mt-2 text-[12px] text-red-600">{{ pricingErr }}</p>
  </section>
</template>
