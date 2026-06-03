import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Info, Archive } from "lucide-react";

import { api, type Node } from "../../api/client";
import { Button, Hint, Input, Modal } from "../ui";

// DeleteNodeDialog — удаление узла (§7.5): чекбокс «также удалить таблицу логов»
// (по умолчанию выкл — таблица остаётся осиротевшей) + подтверждение вводом path.
// «Также удалить таблицу» делается вторым вызовом orphan-drop после удаления узла,
// без изменения API DELETE /nodes.
export function DeleteNodeDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirmText, setConfirmText] = useState("");
  const [dropTable, setDropTable] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const remove = useMutation({
    mutationFn: async () => {
      await api.del(`/api/nodes/${node.id}`);
      if (dropTable && node.clickhouse_table) {
        // Таблица осиротела после удаления узла — дропаем через orphan-эндпоинт.
        try {
          await api.del(
            `/api/settings/clickhouse/orphans/${encodeURIComponent(node.clickhouse_table)}`,
          );
        } catch {
          // Узел уже удалён; ошибку дропа таблицы не считаем фатальной.
        }
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      navigate("/");
    },
    onError: (e: { response?: { data?: { error?: string } } }) =>
      setError(e?.response?.data?.error ?? t("common.error")),
  });

  const canDelete = confirmText === node.path && !remove.isPending;

  return (
    <Modal
      title={<>{t("node.delete.title")} <span className="font-mono">{node.path}</span>?</>}
      onClose={onClose}
      className="max-w-md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" disabled={!canDelete} onClick={() => remove.mutate()}>
            {t("node.delete.confirm")}
          </Button>
        </>
      }
    >
      {error && <div className="mb-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      {node.clickhouse_table && (
        <>
          <label className="mb-2 flex items-center gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={dropTable}
              onChange={(e) => setDropTable(e.target.checked)}
            />
            {t("node.delete.drop_table")}
          </label>
          <Hint tone="muted" icon={<Archive className="h-3.5 w-3.5" />}>
            {dropTable ? t("node.delete.drop_on") : t("node.delete.drop_off")}
            <span className="ml-1 font-mono">{node.clickhouse_table}</span>
          </Hint>
        </>
      )}

      <label className="mb-1.5 mt-4 block text-xs text-fg-muted">
        {t("node.delete.type_to_confirm")} <span className="font-mono">{node.path}</span>
      </label>
      <Input
        mono
        value={confirmText}
        onChange={(e) => setConfirmText(e.target.value)}
        placeholder={node.path}
      />

      {!node.clickhouse_table && (
        <Hint tone="muted" icon={<Info className="h-3.5 w-3.5" />} className="mt-3">
          {t("node.delete.no_table")}
        </Hint>
      )}
    </Modal>
  );
}
