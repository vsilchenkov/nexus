// fmtNum — компактный формат больших чисел (1.24M / 12.0k), разделители для мелких.
export function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (n >= 10_000) return (n / 1000).toFixed(1) + "k";
  return n.toLocaleString();
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
