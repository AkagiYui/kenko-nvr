// Light/dark theme handling. The user picks one of three preferences — light,
// dark, or follow-system — persisted in localStorage. "system" tracks the OS
// color-scheme live via matchMedia. The resolved daisyUI theme name is written
// to <html data-theme>, which is also primed by an inline script in index.html
// to avoid a flash before the app boots.

export type ThemePref = "light" | "dark" | "system";

const KEY = "nvr_theme";
const LIGHT = "kenko-light";
const DARK = "kenko";

export function getThemePref(): ThemePref {
  const v = localStorage.getItem(KEY);
  return v === "light" || v === "dark" || v === "system" ? v : "system";
}

function prefersDark(): boolean {
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function resolve(pref: ThemePref): string {
  if (pref === "system") return prefersDark() ? DARK : LIGHT;
  return pref === "dark" ? DARK : LIGHT;
}

function apply(pref: ThemePref): void {
  document.documentElement.setAttribute("data-theme", resolve(pref));
}

export function setThemePref(pref: ThemePref): void {
  localStorage.setItem(KEY, pref);
  apply(pref);
}

// initTheme applies the stored preference and keeps "system" in sync with the OS.
export function initTheme(): void {
  apply(getThemePref());
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (getThemePref() === "system") apply("system");
  });
}
