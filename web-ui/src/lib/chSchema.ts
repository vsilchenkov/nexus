// chSchema — вспомогательная логика предупреждения о смене схемы ClickHouse (C.1).
//
// Таблица логов создаётся один раз через `CREATE TABLE IF NOT EXISTS`; настройки
// схемы (сжатие/CODEC, data-skipping индексы, движок, native-TTL) применяются
// ТОЛЬКО в момент создания. Синхронизации схемы существующей таблицы с шаблоном
// нет (нет ALTER). Значит смена шаблона у существующего узла БЕЗ смены имени
// таблицы — молчаливый no-op, и оператора надо предупредить.

type SavedNodeCH = {
  clickhouse_template_id: string;
  clickhouse_table: string;
};

// chSchemaChangeWontApply — true, когда предупреждение нужно показать: узел
// существует (не создание), логирование включено, шаблон сменился относительно
// сохранённого, а имя таблицы осталось прежним. При смене имени таблицы схема
// применится (создастся новая таблица), поэтому предупреждение не нужно.
export function chSchemaChangeWontApply(p: {
  isNew: boolean;
  loggingEnabled: boolean;
  currentTemplateId: string;
  currentTable: string;
  saved: SavedNodeCH | null | undefined;
}): boolean {
  if (p.isNew || !p.loggingEnabled || !p.saved) return false;
  return (
    p.currentTemplateId !== p.saved.clickhouse_template_id &&
    p.currentTable === p.saved.clickhouse_table
  );
}
