// §84.8 (узловой срез §80.2): помещается ли узел в одну партицию Kafka.
//
// Receiver публикует в nexus.async с ключом = путь узла ради FIFO по узлу
// (§3.5), поэтому ВСЕ сообщения узла всегда попадают в одну партицию, а внутри
// партиции обрабатываются строго последовательно. Отсюда потолок узла —
// 1 / латентность внешнего вызова, НЕЗАВИСИМО от числа партиций и инстансов
// consumer'а. Доля выше 100 % означает структурный дефицит: лаг растёт
// линейно и сам не рассосётся.
//
// Формула §80.2 дословно: запросов за период × p95 / длительность периода.
// Боевой замер, ради которого раздел и написан: acs_sigur — 219 запросов/ч при
// p95 22,4 с = 136 % ёмкости, и он же блокировал omnichannel (2,7 %),
// захешированный на ту же партицию.

// CAPACITY_OVER — порог, с которого узел в партицию не помещается.
export const CAPACITY_OVER = 1;

/**
 * capacityShare — доля ёмкости партиции, 1 = 100 %.
 *
 * null означает «посчитать не из чего» (нет запросов, нет окна, нет p95) — это
 * НЕ ноль: ноль читался бы как «узел ничего не занимает», а мы просто не знаем.
 */
export function capacityShare(p: {
  total: number;
  p95Ms: number;
  rangeMs: number;
}): number | null {
  if (!Number.isFinite(p.total) || !Number.isFinite(p.p95Ms) || !Number.isFinite(p.rangeMs)) {
    return null;
  }
  if (p.rangeMs <= 0 || p.total <= 0 || p.p95Ms <= 0) return null;
  return (p.total * p.p95Ms) / p.rangeMs;
}

/** capacityTone — как красить долю: выше порога это уже не предупреждение. */
export function capacityTone(share: number | null): "ok" | "warn" | "err" {
  if (share === null) return "ok";
  if (share >= CAPACITY_OVER) return "err";
  if (share >= 0.7 * CAPACITY_OVER) return "warn";
  return "ok";
}

/** fmtShare — «161 %», «2,7 %»: ниже 10 % один знак после запятой значим. */
export function fmtShare(share: number): string {
  const pct = share * 100;
  return pct < 10 ? `${pct.toFixed(1)} %` : `${Math.round(pct)} %`;
}
