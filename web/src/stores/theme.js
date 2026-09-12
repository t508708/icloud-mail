import { computed, ref } from "vue";

export const THEME_STORAGE_KEY = "icloud-admin-theme";
export const THEME_CHOICES = ["light", "dark", "system"];

const preference = ref("system");
const systemDark = ref(false);
let initialized = false;
let mediaQuery;

function readStoredPreference() {
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY);
    return THEME_CHOICES.includes(value) ? value : "system";
  } catch {
    return "system";
  }
}

function applyTheme() {
  if (typeof document === "undefined") return;
  const dark = resolvedTheme.value === "dark";
  document.documentElement.dataset.theme = resolvedTheme.value;
  document.documentElement.classList.toggle("dark", dark);
  document.documentElement.style.colorScheme = resolvedTheme.value;
  document.documentElement.style.backgroundColor = "";
}

const resolvedTheme = computed(() =>
  preference.value === "system" ? (systemDark.value ? "dark" : "light") : preference.value,
);

export function setTheme(value) {
  if (!THEME_CHOICES.includes(value)) return;
  preference.value = value;
  try { window.localStorage.setItem(THEME_STORAGE_KEY, value); } catch { /* storage is optional */ }
  applyTheme();
}

export function initializeTheme() {
  if (initialized) return;
  initialized = true;
  if (typeof window === "undefined") return;
  preference.value = readStoredPreference();
  mediaQuery = window.matchMedia?.("(prefers-color-scheme: dark)");
  systemDark.value = Boolean(mediaQuery?.matches);
  mediaQuery?.addEventListener?.("change", (event) => {
    systemDark.value = Boolean(event.matches);
    if (preference.value === "system") applyTheme();
  });
  window.addEventListener("storage", (event) => {
    if (event.key !== null && event.key !== THEME_STORAGE_KEY) return;
    preference.value = THEME_CHOICES.includes(event.newValue) ? event.newValue : "system";
    applyTheme();
  });
  applyTheme();
}

export function useTheme() {
  initializeTheme();
  return { preference, resolvedTheme, setTheme };
}
