import { render } from "@testing-library/react";
import { afterAll, describe, expect, it } from "vitest";

import i18n from "../../i18n";
import { ImportPreview } from "./ImportPreview";
import type { BackupIssue, ImportPreview as Preview } from "./types";

// Переводы предпросмотра на настоящих словарях. Остальные тесты страницы
// подменяют t на «вернуть ключ» и поэтому не видят ни пропущенного ключа, ни
// сломанной подстановки: на стенде строка масок вышла «( из файла вместо )» —
// параметр назывался replace, а это зарезервированная опция i18next
// (объект подстановок), и она гасила интерполяцию всей строки.

// Все коды проблем плана, которые выставляет сервер
// (internal/web/usecase/backup_import*.go: warn/block/skip).
const ISSUE_CODES = [
  "ch_database_unknown", "ch_database_unclaimed", "ch_database_foreign", "ch_database_conflict",
  "team_slug_too_long", "table_template_missing", "table_ddl_invalid", "template_invalid",
  "log_mask_invalid", "host_invalid", "node_group_invalid", "team_node_limit", "dry_run_failed",
  "ch_database_owned", "ch_database_owned_empty_instance", "ch_database_adopted",
  "ch_database_renamed", "external_table_missing", "external_table_unchecked", "node_invalid",
  "node_self_reference", "table_outside_team_db", "table_foreign_db",
  "settings_clickhouse_not_imported", "settings_field_skipped", "catalog_entry_invalid",
  "user_exists", "api_token_exists", "peer_instance_exists", "peer_instance_self",
];

// Уникальная метка subject: «subj1x» не входит в «subj11x». dry_run_failed
// сервер отдаёт без subject — причина целиком в detail.
const subjectOf = (code: string) =>
  code === "dry_run_failed" ? undefined : `subj${ISSUE_CODES.indexOf(code)}x`;

const issues = (codes: string[]): BackupIssue[] =>
  codes.map((code) => ({ code, subject: subjectOf(code), detail: "7" }));

function plan(): Preview {
  return {
    header: {
      exported_at: "2026-09-29T17:30:00Z",
      nexus_version: "v1.39.0",
      schema_version: 43,
      source_instance_id: "",
      counts: {},
    },
    exported_by: "root",
    source_public_url: "",
    entities: {
      teams: { create: 7, existing: 1 },
      nodes: { create: 172, existing: 0 },
      log_masks: { create: 2, existing: 0, replace: 3 },
    },
    skipped: issues(ISSUE_CODES.slice(20)),
    databases: [{ team: "acme", name: "nexus_acme", ownership: "unclaimed", tables: 4, action: "adopt" }],
    tables: { create: 1, existing: 2, external: 3, external_missing: 0, unmanaged: 0 },
    node_statuses: { enabled: 1, paused: 2, disabled: 3 },
    settings_sections: ["general", "sentry"],
    warnings: issues(ISSUE_CODES.slice(10, 20)),
    blockers: issues(ISSUE_CODES.slice(0, 10)),
    digest: "d",
  };
}

function renderIn(lng: string): string {
  void i18n.changeLanguage(lng);
  const { container, unmount } = render(
    <ImportPreview
      preview={plan()}
      options={{ status_mode: "pause_pull", adopt_unclaimed: false }}
      onOptionsChange={() => {}}
      disabled={false}
    />,
  );
  const text = container.textContent ?? "";
  unmount();
  return text;
}

afterAll(() => {
  void i18n.changeLanguage("en");
});

describe("ImportPreview — настоящие переводы", () => {
  it("строка масок подставляет оба числа", () => {
    expect(renderIn("ru")).toContain("маски логов (2 из файла вместо 3)");
    expect(renderIn("en")).toContain("log masks (2 from the file instead of 3)");
  });

  it.each(["ru", "en"])("%s: нет сырых ключей и неподставленных {{…}}", (lng) => {
    const text = renderIn(lng);
    expect(text).not.toMatch(/settings\.backup\./);
    expect(text).not.toContain("{{");
    // Каждая проблема переведена, а не показана кодом (defaultValue), и её
    // subject подставлен.
    for (const code of ISSUE_CODES) {
      expect(text).not.toContain(code);
      const subject = subjectOf(code);
      if (subject) expect(text).toContain(subject);
    }
  });
});
