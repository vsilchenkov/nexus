import { useTranslation } from "react-i18next";

import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { CopyButton } from "../ui/CopyButton";

// §83: подробная справка по шаблону ответа приёма.
//
// Технические токены (body.*, max, ${...}, сами шаблоны) — константы здесь, а
// не ключи локализации: переводить их нельзя, а держать в json — значит рано
// или поздно перевести. В i18n уходят только описания.

type Props = {
  onClose: () => void;
  // onUse — вставить пример в поле шаблона и закрыть справку. Примеры обязаны
  // быть рабочими: оператор нажимает «Использовать» и сразу жмёт «Проверить».
  onUse: (template: string) => void;
};

type Row = { token: string; descKey: string };

const SOURCES: Row[] = [
  { token: "body.*", descKey: "node.ack.help.src_body" },
  { token: "query.<имя>", descKey: "node.ack.help.src_query" },
  { token: "path.suffix", descKey: "node.ack.help.src_path_suffix" },
  { token: "path.segment[n]", descKey: "node.ack.help.src_path_segment" },
  { token: "nexus.id", descKey: "node.ack.help.src_nexus_id" },
  { token: "nexus.node", descKey: "node.ack.help.src_nexus_node" },
  { token: "nexus.team", descKey: "node.ack.help.src_nexus_team" },
  { token: "nexus.received_at", descKey: "node.ack.help.src_nexus_received_at" },
  { token: "nexus.queued", descKey: "node.ack.help.src_nexus_queued" },
];

const FUNCTIONS: Row[] = [
  { token: "max", descKey: "node.ack.help.fn_max" },
  { token: "min", descKey: "node.ack.help.fn_min" },
  { token: "first", descKey: "node.ack.help.fn_first" },
  { token: "last", descKey: "node.ack.help.fn_last" },
  { token: "count", descKey: "node.ack.help.fn_count" },
  { token: "default(x)", descKey: "node.ack.help.fn_default" },
];

const PATHS: Row[] = [
  { token: "a.b.c", descKey: "node.ack.help.path_dot" },
  { token: "a[0]", descKey: "node.ack.help.path_index" },
  { token: "a[*]", descKey: "node.ack.help.path_all" },
  { token: 'a["ключ.с.точкой"]', descKey: "node.ack.help.path_quoted" },
];

const LIMITS: Row[] = [
  { token: "8 KiB", descKey: "node.ack.help.limit_template" },
  { token: "32", descKey: "node.ack.help.limit_placeholders" },
  { token: "1 MiB", descKey: "node.ack.help.limit_body" },
  { token: "64 KiB", descKey: "node.ack.help.limit_output" },
  { token: "1000", descKey: "node.ack.help.limit_values" },
  { token: "16", descKey: "node.ack.help.limit_depth" },
];

const REASONS: Row[] = [
  { token: "body_not_json", descKey: "node.ack.reason_fix.body_not_json" },
  { token: "body_too_large", descKey: "node.ack.reason_fix.body_too_large" },
  { token: "path_not_found", descKey: "node.ack.reason_fix.path_not_found" },
  { token: "type_mismatch", descKey: "node.ack.reason_fix.type_mismatch" },
  { token: "ambiguous", descKey: "node.ack.reason_fix.ambiguous" },
  { token: "too_many_values", descKey: "node.ack.reason_fix.too_many_values" },
  { token: "output_too_big", descKey: "node.ack.reason_fix.output_too_big" },
  { token: "output_not_json", descKey: "node.ack.reason_fix.output_not_json" },
];

const APPLIES: Row[] = [
  { token: "async 200", descKey: "node.ack.help.applies_async" },
  { token: "202 (§3.6)", descKey: "node.ack.help.applies_paused" },
  { token: "callback (§16)", descKey: "node.ack.help.applies_callback" },
  { token: "404 / 405 / 413 / 5xx", descKey: "node.ack.help.applies_errors" },
  { token: "sync", descKey: "node.ack.help.applies_sync" },
];

const EXAMPLES: Array<{ titleKey: string; template: string }> = [
  {
    titleKey: "node.ack.help.ex_cursor",
    template: '{"confirmedLogId": "${ body.logs[*].logId | max }"}',
  },
  {
    titleKey: "node.ack.help.ex_cursor_count",
    template:
      '{"confirmedLogId": "${ body.logs[*].logId | max }", "accepted": "${ body.logs[*].logId | count }"}',
  },
  { titleKey: "node.ack.help.ex_plain", template: "${ query.code }" },
  {
    titleKey: "node.ack.help.ex_echo_id",
    template: '{"ok": true, "requestId": "${ nexus.id }"}',
  },
];

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-4 first:mt-0">
      <h4 className="mb-1.5 text-[13px] font-medium text-fg">{title}</h4>
      <div className="text-xs leading-relaxed text-fg-muted">{children}</div>
    </section>
  );
}

function TokenTable({ rows }: { rows: Row[] }) {
  const { t } = useTranslation();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <tbody>
          {rows.map((r) => (
            <tr key={r.token} className="align-top">
              <td className="whitespace-nowrap py-1 pr-3 font-mono text-fg">{r.token}</td>
              <td className="py-1 text-fg-muted">{t(r.descKey)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Code({ children }: { children: string }) {
  return (
    <pre className="mt-1 overflow-x-auto rounded-md border border-line bg-app px-3 py-2 font-mono text-xs">
      {children}
    </pre>
  );
}

export function AckHelpDialog({ onClose, onUse }: Props) {
  const { t } = useTranslation();

  return (
    <Modal
      title={t("node.ack.help.title")}
      subtitle={t("node.ack.help.subtitle")}
      onClose={onClose}
      footer={
        <Button type="button" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      <div className="max-h-[65vh] overflow-y-auto pr-1">
        <Section title={t("node.ack.help.what_title")}>{t("node.ack.help.what_text")}</Section>

        <Section title={t("node.ack.help.applies_title")}>
          <TokenTable rows={APPLIES} />
        </Section>

        <Section title={t("node.ack.help.fields_title")}>
          <ul className="list-disc space-y-1 pl-4">
            <li>{t("node.ack.help.field_format")}</li>
            <li>{t("node.ack.help.field_status")}</li>
            <li>{t("node.ack.help.field_on_error")}</li>
            <li>{t("node.ack.help.field_body")}</li>
          </ul>
        </Section>

        <Section title={t("node.ack.help.syntax_title")}>
          {t("node.ack.help.syntax_text")}
          <Code>{"${ источник.путь | функция }"}</Code>
          {t("node.ack.help.syntax_escape")}
        </Section>

        <Section title={t("node.ack.help.sources_title")}>
          <TokenTable rows={SOURCES} />
          <p className="mt-1.5 text-warn">{t("node.ack.help.sources_no_headers")}</p>
        </Section>

        <Section title={t("node.ack.help.paths_title")}>
          <TokenTable rows={PATHS} />
        </Section>

        <Section title={t("node.ack.help.functions_title")}>
          <TokenTable rows={FUNCTIONS} />
        </Section>

        <Section title={t("node.ack.help.quotes_title")}>
          {t("node.ack.help.quotes_text")}
          <Code>{'{"confirmedLogId": "${ body.logs[*].logId | max }"}  →  {"confirmedLogId": 79154}'}</Code>
          <Code>{'{"msg": "принято ${ body.n }"}  →  {"msg": "принято 12"}'}</Code>
          <p className="mt-1.5">{t("node.ack.help.quotes_text_plain")}</p>
        </Section>

        <Section title={t("node.ack.help.examples_title")}>
          <div className="space-y-3">
            {EXAMPLES.map((ex) => (
              <div key={ex.titleKey}>
                <div className="flex items-center justify-between gap-2">
                  <span>{t(ex.titleKey)}</span>
                  <div className="flex items-center gap-2">
                    <CopyButton value={ex.template} />
                    <button
                      type="button"
                      onClick={() => onUse(ex.template)}
                      className="whitespace-nowrap text-xs text-accent hover:underline"
                    >
                      {t("node.ack.help.use")}
                    </button>
                  </div>
                </div>
                <Code>{ex.template}</Code>
              </div>
            ))}
          </div>
        </Section>

        <Section title={t("node.ack.help.limits_title")}>
          <TokenTable rows={LIMITS} />
        </Section>

        <Section title={t("node.ack.help.reasons_title")}>
          <TokenTable rows={REASONS} />
        </Section>

        <Section title={t("node.ack.help.meaning_title")}>
          <p className="text-warn">{t("node.ack.help.meaning_text")}</p>
          <p className="mt-1.5">{t("node.ack.help.meaning_apply")}</p>
        </Section>
      </div>
    </Modal>
  );
}
