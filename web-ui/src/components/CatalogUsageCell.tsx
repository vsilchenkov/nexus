// CatalogUsageCell — ячейка «узлов: N» в таблице справочника (§104.5):
// используемый элемент — кнопка, открывающая список узлов, неиспользуемый —
// просто текст (открывать нечего).
//
// Диалог держит страница, а не строка: после перечитывания таблицы строка может
// исчезнуть (элемент удалён) или смениться на «не используется», и диалог,
// живущий в строке, закрылся бы на полуслове.
export function CatalogUsageCell({
  count,
  usedLabel,
  notUsedLabel,
  onOpen,
}: {
  count: number;
  usedLabel: string;
  notUsedLabel: string;
  onOpen: () => void;
}) {
  if (count <= 0) return <>{notUsedLabel}</>;
  return (
    <button
      type="button"
      aria-haspopup="dialog"
      onClick={onOpen}
      className="rounded text-accent hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
    >
      {usedLabel}
    </button>
  );
}
