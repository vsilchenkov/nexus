// Тема панели: тёмная (эталон) / светлая. Хранится в localStorage,
// применяется классом `dark` на <html>. Переключатель — в топбаре (§21).
export type Theme = "dark" | "light";

const KEY = "nexus.theme";

export function getTheme(): Theme {
  return localStorage.getItem(KEY) === "light" ? "light" : "dark";
}

export function applyTheme(t: Theme): void {
  document.documentElement.classList.toggle("dark", t === "dark");
}

export function setTheme(t: Theme): void {
  localStorage.setItem(KEY, t);
  applyTheme(t);
}
