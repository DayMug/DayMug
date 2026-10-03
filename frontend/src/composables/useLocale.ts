import { computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  SUPPORTED_LOCALES,
  isLocaleLoaded,
  loadLocaleMessages,
  persistLocale,
  toSupportedLocale,
  type SupportedLocale,
} from "@/i18n";

// Thin wrapper around vue-i18n that also persists the user's choice to
// localStorage so the next page load (the login → app reload, in particular)
// keeps the language they picked.
export function useLocale() {
  const { locale, t } = useI18n();

  const current = computed<SupportedLocale>(() => {
    const v = locale.value as string;
    return toSupportedLocale(v);
  });

  // Switch only once the translation is registered, so the UI never flashes
  // through English on the way. Already-loaded locales switch synchronously.
  function setLocale(next: SupportedLocale): Promise<void> {
    if (!SUPPORTED_LOCALES.includes(next)) return Promise.resolve();
    persistLocale(next);
    const apply = () => {
      locale.value = next;
    };
    if (isLocaleLoaded(next)) {
      apply();
      return Promise.resolve();
    }
    return loadLocaleMessages(next).then(apply, (err: unknown) => {
      console.error(`[i18n] failed to load "${next}" messages`, err);
    });
  }

  function toggleLocale() {
    void setLocale(current.value === "zh" ? "en" : "zh");
  }

  return { locale: current, setLocale, toggleLocale, t };
}
