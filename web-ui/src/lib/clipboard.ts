// copyToClipboard — запись строки в буфер обмена с graceful fallback на
// document.execCommand для не-secure контекстов (§2/§58). Возвращает true при
// успехе, false если буфер недоступен — вызывающий решает, показывать ли
// подтверждение. Единый источник clipboard-логики: используют CopyButton и
// ShareNodeButton.
export async function copyToClipboard(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
    } else {
      const ta = document.createElement("textarea");
      ta.value = value;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    return true;
  } catch {
    return false;
  }
}
