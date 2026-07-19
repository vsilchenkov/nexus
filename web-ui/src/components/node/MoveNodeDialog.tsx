import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api, type Node } from "../../api/client";
import { Button, Field, Select } from "../ui";
import { Modal } from "../ui/Modal";

type Team = { id: string; slug: string; name: string };

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

  const move = useMutation({
    mutationFn: () => api.post(`/api/nodes/${node.id}/move`, { target_team_slug: slug }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      // Именно remove, а не invalidate: узел уехал в чужую команду, и рефетч
      // по team-scoped GET заведомо вернёт 404 (лишний запрос + ошибка в консоли).
      qc.removeQueries({ queryKey: ["node", node.id] });
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
    </Modal>
  );
}
