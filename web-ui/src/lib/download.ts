// REVOKE_DELAY_MS — через сколько освобождать blob-URL после клика. Немедленный
// revokeObjectURL рвёт ещё не начавшееся сохранение в Firefox (файл приходит
// пустым или «Ошибка загрузки»): click() лишь ставит загрузку в очередь.
const REVOKE_DELAY_MS = 60_000;

// saveBlob — отдать файл браузеру на сохранение (§110 резервная копия, §115
// выгрузка журнала). Память blob'а освобождается после REVOKE_DELAY_MS.
export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), REVOKE_DELAY_MS);
}
