import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api, type Node } from "../../api/client";
import { Button, Field, Select } from "../ui";
import { Modal } from "../ui/Modal";

type Team = { id: string; slug: string; name: string };

// MovePreview — что случится с таблицей логов (GET /api/nodes/:id/move-preview).
type MovePreview = {
  target_table: string;
  table_shared: boolean;
  shared_with: number;
  target_table_exists: boolean;
};

// MoveNodeDialog — перенос узла в другую команду (admin-only эндпоинт
// POST /api/nodes/:id/move). Вызывается из формы правки узла (NodeSettings);
// onMoved даёт вызывающему довести навигацию — после переноса узел уходит в
// чужую команду и текущая страница правки становится недоступной.
export function MoveNodeDialog({
  node,
  onClose,
  onMoved,
}: {
  node: Node;
  onClose: () => void;
  onMoved?: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [error, setError] = useState<string | null>(null);

  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => api.get<{ items: Team[] }>("/api/teams"),
  });

  // Предпросмотр судьбы таблицы логов — запрашивается только когда команда
  // выбрана: у переноса два неочевидных исхода, и оба меняют то, какие логи
  // узел покажет после переезда.
  const preview = useQuery({
    queryKey: ["node-move-preview", node.id, slug],
    queryFn: () =>
      api.get<MovePreview>(`/api/nodes/${node.id}/move-preview`, { target_team_slug: slug }),
    enabled: slug !== "",
  });

  const move = useMutation({
    mutationFn: () => api.post(`/api/nodes/${node.id}/move`, { target_team_slug: slug }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      // Именно remove, а не invalidate: узел уехал в чужую команду, и рефетч
      // по team-scoped GET заведомо вернёт 404 (лишний запрос + ошибка в консоли).
      qc.removeQueries({ queryKey: ["node", node.id] });
      // Резолвер команды узла (§58) отдал бы из кеша команду ДО переноса: имя в
      // строке «Команда» — старое, а useEnsureNodeTeam на открытии узла увёл бы
      // сессию обратно в исходную команду. Ключ выбрасываем целиком.
      qc.removeQueries({ queryKey: ["node-team", node.id] });
      onClose();
      onMoved?.();
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <Modal
      title={t("overview.move.title")}
      subtitle={node.path}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={!slug || move.isPending} onClick={() => move.mutate()}>
            {t("overview.move.submit")}
          </Button>
        </>
      }
    >
      {error && (
        <div className="mb-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
      )}
      <Field label={t("overview.move.target_team")} hint={t("overview.move.hint")}>
        <Select value={slug} onChange={(e) => setSlug(e.target.value)}>
          <option value="">{t("overview.move.pick_team")}</option>
          {(teams.data?.items ?? []).map((tm) => (
            <option key={tm.id} value={tm.slug}>
              {tm.name} ({tm.slug})
            </option>
          ))}
        </Select>
      </Field>
      {preview.data && <MoveTablePreview preview={preview.data} />}
    </Modal>
  );
}

// MoveTablePreview — что станет с таблицей логов после переноса. Показываем
// только неочевидные исходы: обычный переезд таблицы вместе с узлом и так
// описан подсказкой над полем.
function MoveTablePreview({ preview }: { preview: MovePreview }) {
  const { t } = useTranslation();
  const notes: string[] = [];
  if (preview.table_shared) {
    notes.push(t("overview.move.preview.shared", { count: preview.shared_with }));
  }
  if (preview.target_table_exists) {
    notes.push(t("overview.move.preview.target_exists", { table: preview.target_table }));
  }
  if (notes.length === 0) {
    return null;
  }
  return (
    <div className="mt-3 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-[12px] text-warn">
      <div className="mb-1 font-medium">{t("overview.move.preview.title")}</div>
      <ul className="list-disc space-y-1 pl-4">
        {notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </div>
  );
}
