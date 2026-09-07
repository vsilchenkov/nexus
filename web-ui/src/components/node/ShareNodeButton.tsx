import { ShareLinkButton } from "../ui";
import { nodePageUrl } from "../../lib/nodeShare";
import { type NodeTab } from "../../lib/nodeTabUrl";

// ShareNodeButton — кнопка «Поделиться» страницей узла (§58, п.1). Ссылка
// team-независима: открывший её коллега авто-переключится на команду узла
// (useEnsureNodeTeam, §58 п.2).
//
// §79.3: tab — текущая вкладка; ссылка ведёт сразу на неё («вот метрики этого
// узла»), а не на «Обзор», с которого коллеге пришлось бы кликать заново.
//
// §98.3: копирование и подтверждение живут в общем ShareLinkButton — здесь
// осталась только сборка адреса, специфичная для узла.
export function ShareNodeButton({
  nodeId,
  tab,
  search,
  sm,
}: {
  nodeId: string;
  tab?: NodeTab;
  // §84.2: текущие параметры адреса — из них в ссылку попадает вид вкладки
  // (период и шаг «Метрик»). Отбор ключей делает nodeTabUrl, не эта кнопка.
  search?: URLSearchParams;
  sm?: boolean;
}) {
  return <ShareLinkButton url={nodePageUrl(nodeId, tab, search)} sm={sm} />;
}
