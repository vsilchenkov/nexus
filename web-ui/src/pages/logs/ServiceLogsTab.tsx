import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowDown, ChevronDown, ChevronRight } from "lucide-react";

import { api } from "../../api/client";
import { cn } from "../../lib/cn";
import { LogsToolbar } from "../../components/logs/LogsToolbar";
import { ErrorAlert } from "../../components/ui";
import {
  filterEntries,
  hasDistinctReplicas,
  formatTs,
  levelBadgeClass,
  levelFromInt,
  levelToInt,
  logsQueryKey,
  serviceParam,
  stableStringify,
  type LevelName,
  type ServiceLogEntry,
  type ServiceName,
} from "../../lib/logsUtils";

type AppSettings = { logging?: { level?: number } };

const LIVE_REFETCH_MS = 2500;
// Кап DOM-строк консоли (§51.6) — сервер и так отдаёт не больше limit≤2000.
const MAX_ROWS = 2000;

// ServiceLogsTab — консоль служебных логов трёх сервисов (§51.6, admin-only):
// follow-tail поток (новые снизу, автоскролл при Live, пауза при ручном
// скролле вверх), чипсы сервисов, сегмент уровня (меняет РЕАЛЬНЫЙ порог),
// поиск, скачивание файла, раскрытие строки в атрибуты.
export default function ServiceLogsTab() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const [services, setServices] = useState<ServiceName[]>([]); // пусто = все
  const [query, setQuery] = useState("");
  const [live, setLive] = useState(true);
  const [limit, setLimit] = useState(200);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [stick, setStick] = useState(true);

  // Фильтр сервисов — СЕРВЕРНЫЙ (limit применяется к выбранным сервисам).
  // Клиентским он был бы бесполезен: «болтливый» сервис выбирает весь limit
  // при мерже, и строки остальных до браузера не доезжают (поймано на стенде).
  const svcParam = serviceParam(services);
  const logs = useQuery({
    queryKey: logsQueryKey(services, limit),
    queryFn: () =>
      api.get<ServiceLogEntry[]>("/api/logs", svcParam ? { limit, service: svcParam } : { limit }),
    refetchInterval: live ? LIVE_REFETCH_MS : false,
  });

  const settings = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });
  const level = levelFromInt(settings.data?.logging?.level);
  const setLevel = useMutation({
    mutationFn: (l: LevelName) => api.put("/api/settings/app", { logging: { level: levelToInt(l) } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["app-settings"] }),
  });

  // Сервер отдаёт свежие первыми; консоль рисует хронологически (новые снизу).
  const visible = useMemo(() => {
    const filtered = filterEntries(logs.data ?? [], { query });
    return filtered.slice(0, MAX_ROWS).reverse();
  }, [logs.data, query]);

  // §93.6: колонка реплики появляется сама, когда в выборке есть записи от
  // разных экземпляров сервиса. В одиночной установке её нет — там имя реплики
  // повторяло бы имя сервиса.
  const showReplica = useMemo(() => hasDistinctReplicas(visible), [visible]);

  // Follow-tail: пока «прилипли» к низу — держим низ при каждом обновлении.
  const scrollRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stick) el.scrollTop = el.scrollHeight;
  }, [visible, stick]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    setStick(atBottom);
  };

  const downloadHref = useMemo(() => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (svcParam) params.set("service", svcParam);
    return `/api/logs/download?${params.toString()}`;
  }, [svcParam, limit]);

  const rowKey = (e: ServiceLogEntry, i: number) => `${e.ts}|${e.service}|${i}`;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Заголовок раздела и вкладки живут в контейнере (§94.7) — здесь
          остаётся только пояснение, что это за поток: путать служебные логи
          процессов с журналом чужих запросов §51.1 прямо запрещает. */}
      <div className="flex items-center gap-2 pb-2">
        <span className="text-xs text-fg-muted">{t("logs.viewer.subtitle")}</span>
      </div>

      <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-line bg-bg">
        <LogsToolbar
          services={services}
          onServicesChange={setServices}
          level={level}
          onLevelChange={(l) => setLevel.mutate(l)}
          levelPending={setLevel.isPending || settings.isLoading}
          query={query}
          onQueryChange={setQuery}
          live={live}
          onLiveToggle={() => setLive((v) => !v)}
          limit={limit}
          onLimitChange={setLimit}
          onRefresh={() => void logs.refetch()}
          downloadHref={downloadHref}
        />

        <div
          ref={scrollRef}
          onScroll={onScroll}
          className="min-h-0 flex-1 overflow-y-auto px-2 py-1 font-mono text-[12px] leading-5"
        >
          {logs.isLoading && <div className="p-3 text-fg-muted">{t("common.loading")}</div>}
          {logs.isError && (
            <div className="p-3">
              <ErrorAlert />
            </div>
          )}
          {!logs.isLoading && !logs.isError && visible.length === 0 && (
            <div className="p-3 text-fg-muted">{t("logs.viewer.empty")}</div>
          )}

          {visible.map((e, i) => {
            const key = rowKey(e, i);
            const isOpen = expanded === key;
            const hasAttrs = e.attrs != null && Object.keys(e.attrs).length > 0;
            return (
              <div key={key} className="border-b border-line/40 last:border-0">
                <button
                  type="button"
                  onClick={() => setExpanded(isOpen ? null : key)}
                  className={cn(
                    "flex w-full items-start gap-2 px-1 py-0.5 text-left hover:bg-bg-muted",
                    isOpen && "bg-bg-muted",
                  )}
                >
                  <span className="mt-0.5 shrink-0 text-fg-subtle">
                    {hasAttrs ? (
                      isOpen ? (
                        <ChevronDown className="h-3 w-3" />
                      ) : (
                        <ChevronRight className="h-3 w-3" />
                      )
                    ) : (
                      <span className="inline-block w-3" />
                    )}
                  </span>
                  <span className="shrink-0 text-fg-subtle">{formatTs(e.ts)}</span>
                  <span className={cn("w-12 shrink-0 uppercase", levelBadgeClass(e.level))}>
                    {e.level}
                  </span>
                  <span className="w-16 shrink-0 text-fg-muted">{e.service}</span>
                  {showReplica && (
                    <span className="w-20 shrink-0 truncate text-fg-subtle" title={e.replica ?? ""}>
                      {e.replica ?? ""}
                    </span>
                  )}
                  <span className="min-w-0 flex-1 whitespace-pre-wrap break-words text-fg">
                    {e.msg}
                  </span>
                </button>
                {isOpen && hasAttrs && (
                  <div className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 px-8 py-1.5 text-[11.5px]">
                    {Object.entries(e.attrs ?? {}).map(([k, v]) => (
                      <div key={k} className="contents">
                        <span className="text-fg-subtle">{k}</span>
                        <span className="break-all text-fg-muted">
                          {typeof v === "object" && v !== null ? stableStringify(v) : String(v)}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>

        {!stick && (
          <button
            type="button"
            onClick={() => {
              const el = scrollRef.current;
              if (el) el.scrollTop = el.scrollHeight;
              setStick(true);
            }}
            className="absolute bottom-4 right-6 flex items-center gap-1.5 rounded-full border border-line bg-bg-elev px-3 py-1.5 text-xs text-fg shadow-md hover:bg-bg-muted"
          >
            <ArrowDown className="h-3.5 w-3.5" /> {t("logs.viewer.to_latest")}
          </button>
        )}
      </div>
    </div>
  );
}
