import { MessageSquare } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "../lib/cn";
import type { CommentSnippet as Snippet } from "../lib/commentSnippet";

// CommentSnippet — строка «совпадение в комментарии» (§103.4): иконка с
// доступным именем (отличает строку от адреса узла) и фрагмент комментария с
// подсвеченным совпадением. lines=2 — для карточки, где строка может занять две
// строки; по умолчанию одна строка с многоточием по ширине.
export function CommentSnippet({
  snippet,
  lines = 1,
  className,
}: {
  snippet: Snippet;
  lines?: 1 | 2;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <span className={cn("flex min-w-0 items-start gap-1 text-[11px] text-fg-subtle", className)}>
      <span role="img" aria-label={t("search.comment_match")} className="mt-[2px] shrink-0">
        <MessageSquare className="h-3 w-3" aria-hidden />
      </span>
      <span className={cn("min-w-0 break-words", lines === 1 ? "truncate" : "line-clamp-2")}>
        {snippet.before}
        <mark className="rounded-sm bg-warn/20 px-0.5 text-fg">{snippet.match}</mark>
        {snippet.after}
      </span>
    </span>
  );
}
