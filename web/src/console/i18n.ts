import { ref } from "vue";
import english from "./en.json";

export type Locale = "ru" | "en";
const storageKey = "talosdeck.console.language";
function initialLocale(): Locale {
  try {
    return localStorage.getItem(storageKey) === "en" ? "en" : "ru";
  } catch {
    return "ru";
  }
}
export const locale = ref<Locale>(initialLocale());
document.documentElement.lang = locale.value;
export function setLocale(value: string) {
  if (value !== "ru" && value !== "en") return;
  locale.value = value;
  document.documentElement.lang = value;
  try {
    localStorage.setItem(storageKey, value);
  } catch {
    /* Storage can be disabled. */
  }
}
// Russian source strings are stable translation keys. API data, logs and config
// are never translated; values are interpolated as text, never as HTML.
export function t(source: string, values: unknown[] = []): string {
  const text =
    locale.value === "en"
      ? ((english as Record<string, string>)[source] ?? source)
      : source;
  return text.replace(/\{(\d+)\}/g, (placeholder, index) =>
    Number(index) < values.length ? String(values[Number(index)]) : placeholder,
  );
}
