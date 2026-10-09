import { createI18n } from "vue-i18n";
import en from "./locales/en";

export type SupportedLocale = "en" | "zh";
export type InitialLocale = string;

export const SUPPORTED_LOCALES: SupportedLocale[] = ["en", "zh"];
export const DEFAULT_LOCALE: SupportedLocale = "en";
export const LOCALE_STORAGE_KEY = "daymug.lang";

// Resolve initial locale: explicit user choice from localStorage takes
// precedence; otherwise use the browser's first preferred language. vue-i18n
// will resolve known language-family fallbacks (zh-CN -> zh, en-GB -> en) and
// use DEFAULT_LOCALE only when we have no browser signal.
export function detectInitialLocale(): InitialLocale {
  try {
    const stored = window.localStorage.getItem(LOCALE_STORAGE_KEY);
    if (stored === "en" || stored === "zh") return stored;
  } catch {
    // localStorage may be unavailable (private mode quirks). Fall through.
  }
  const langs: string[] = [];
  if (typeof navigator !== "undefined") {
    if (Array.isArray(navigator.languages)) langs.push(...navigator.languages);
    if (navigator.language) langs.push(navigator.language);
  }
  for (const raw of langs) {
    if (typeof raw === "string" && raw.trim()) {
      return raw;
    }
  }
  return DEFAULT_LOCALE;
}

// Map any BCP 47 tag onto the translation that serves it.
export function toSupportedLocale(locale: string): SupportedLocale {
  return locale.toLowerCase().startsWith("zh") ? "zh" : "en";
}

// Only English (the fallback every missing key resolves to) ships in the entry
// chunk; each other translation is its own chunk, fetched before the app mounts
// in that language or when the user switches to it.
const LAZY_LOCALES: Record<Exclude<SupportedLocale, "en">, () => Promise<{ default: object }>> = {
  zh: () => import("./locales/zh"),
};

export const i18n = createI18n<Record<string, unknown>, string, false>({
  legacy: false,
  locale: detectInitialLocale(),
  fallbackLocale: DEFAULT_LOCALE,
  messages: { en },
});

const localeLoads = new Map<SupportedLocale, Promise<void>>();

export function isLocaleLoaded(locale: SupportedLocale): boolean {
  return i18n.global.availableLocales.includes(locale);
}

// Resolves once `locale`'s messages are registered. A failed fetch is not
// cached, so the next attempt retries; until then keys fall back to English.
export function loadLocaleMessages(locale: SupportedLocale): Promise<void> {
  if (locale === "en" || isLocaleLoaded(locale)) return Promise.resolve();
  let pending = localeLoads.get(locale);
  if (!pending) {
    pending = LAZY_LOCALES[locale]()
      .then((mod) => {
        i18n.global.setLocaleMessage(locale, mod.default as Record<string, unknown>);
      })
      .finally(() => localeLoads.delete(locale));
    localeLoads.set(locale, pending);
  }
  return pending;
}

export function persistLocale(locale: SupportedLocale): void {
  try {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // Best effort — settings simply won't survive across reloads.
  }
}
