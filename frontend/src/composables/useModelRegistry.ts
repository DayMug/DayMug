import { ref } from "vue";

import { fetchModels } from "./apiModels";
import type { ModelRegistry, ProviderCapabilities, ProviderEntry } from "./apiTypes";

// Module-level state: /api/models is small, so the whole app shares one fetch.
// Admin edits invalidate it explicitly; concurrent callers still reuse the
// in-flight promise so we never fire duplicate requests.
const registry = ref<ModelRegistry | null>(null);
let pending: Promise<ModelRegistry> | null = null;
let generation = 0;

// loadModelRegistry returns the cached registry, fetching it on first
// call. Subsequent callers get the same promise / ref. Failed fetches
// leave both ref and pending null so a retry can try again — callers
// treat null as "no capability data yet" and keep their defaults.
export async function loadModelRegistry(): Promise<ModelRegistry> {
  if (registry.value) return registry.value;
  if (!pending) {
    const requestGeneration = generation;
    pending = fetchModels()
      .then((r) => {
        if (requestGeneration === generation) registry.value = r;
        return r;
      })
      .catch((err) => {
        if (requestGeneration === generation) pending = null;
        throw err;
      });
  }
  return pending;
}

// invalidateModelRegistry makes the next picker/capability consumer refetch
// after an admin changes provider accounts or their model lists.
export function invalidateModelRegistry() {
  generation++;
  registry.value = null;
  pending = null;
}

// useModelRegistry exposes the cached ref for templates / composables.
// The .value field is null until the first loadModelRegistry()
// resolves; ensureLoaded() returns a promise callers can await when
// they need it synchronously (parser dispatch, for instance).
export function useModelRegistry() {
  return {
    registry,
    ensureLoaded: loadModelRegistry,
  };
}

// providerCapabilities looks up the capability matrix for a given
// provider name (e.g. "claude" / "codex"). Returns null when the
// registry hasn't loaded yet OR the provider is missing from it;
// callers treat both cases as "no gating".
export function providerCapabilities(
  reg: ModelRegistry | null,
  providerName: string,
): ProviderCapabilities | null {
  if (!reg || !providerName) return null;
  const p: ProviderEntry | undefined = reg.providers.find((e) => e.name === providerName);
  return p?.capabilities ?? null;
}

// modelAcceptsImages reports whether the admin left image input enabled for a
// model on an account. Unknown accounts/models count as accepting images —
// warning on missing data would cry wolf on every older registry payload.
export function modelAcceptsImages(
  reg: ModelRegistry | null,
  provider: string,
  account: string,
  model: string,
): boolean {
  const entry = reg?.accounts.find((a) => a.provider === provider && a.account === account);
  return !entry?.specs[model]?.no_image_input;
}
