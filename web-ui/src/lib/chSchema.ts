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
  clickhouse_retention_days: number;
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

// chSyncFormDirty — true, когда CH-поля формы (шаблон / имя таблицы / retention)
// отличаются от сохранённых. Синхронизация схемы (§56) планирует ALTER'ы по
// СОХРАНЁННОМУ узлу (эндпоинт /ch-schema/plan берёт только id, значения формы
// туда не уходят), поэтому при «грязной» форме кнопку синхронизации надо сперва
// предложить сохранить — иначе оператор правит поле, жмёт «Синхронизировать» и
// видит «уже соответствует» по старым данным.
export function chSyncFormDirty(p: {
  isNew: boolean;
  currentTemplateId: string;
  currentTable: string;
  currentRetentionDays: number;
  saved: SavedNodeCH | null | undefined;
}): boolean {
  if (p.isNew || !p.saved) return false;
  return (
    p.currentTemplateId !== p.saved.clickhouse_template_id ||
    p.currentTable !== p.saved.clickhouse_table ||
    p.currentRetentionDays !== p.saved.clickhouse_retention_days
  );
}
