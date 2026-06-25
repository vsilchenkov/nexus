// fmtNum — компактный формат больших чисел (1.24M / 12.0k), разделители для мелких.
export function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (n >= 10_000) return (n / 1000).toFixed(1) + "k";
  return n.toLocaleString();
}

// msToDatetimeLocal — UnixMilli → строка для <input type="datetime-local">
// (YYYY-MM-DDTHH:mm в локальной зоне). Используется для проброса границ бакета
// графика в фильтр логов (§33.4).
export function msToDatetimeLocal(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

// fmtLogTs — компактная дата+время лога "DD.MM HH:MM:SS" (24ч, локальная зона,
// без года). Детерминированный формат (не toLocale*) — фиксированная ширина,
// чтобы колонка времени в таблице логов не ехала по разметке (§44.J).
export function fmtLogTs(dateStr: string): string {
  const d = new Date(dateStr);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getDate())}.${p(d.getMonth() + 1)} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// fmtBytes — человекочитаемый размер на диске (12.5 GB). 0 → «—» (размер
// топика недоступен через high-level Kafka API, см. §4.3 spec).
export function fmtBytes(n: number): string {
  if (n <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
