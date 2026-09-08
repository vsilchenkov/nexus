// Группировка списка узлов по группам (§99.5).
//
// Главное свойство модуля: он НЕ сортирует. Он стабильно разбивает уже
// отсортированный массив узлов на секции, сохраняя взаимный порядок внутри
// каждой. Отсюда два бесплатных следствия:
//   - правило «проблемные первыми» (§22.5) начинает действовать внутри группы,
//     а не поверх всего списка;
//   - заморозка порядка сквозного режима (§86.10) продолжает работать без
//     изменений — переставлять строки под курсором тут нечему.
// Сортировка «сначала по группе, потом по статусу» потребовала бы дублировать
// обе ветки сортировки Overview — живую и замороженную.
import { type Node, type NodeGroup } from "../api/client";

export type NodeSection = {
  // group — null для секции узлов без группы. Она всегда первая и без
  // заголовка: если групп в инсталляции нет, экран выглядит как до §99.
  group: NodeGroup | null;
  nodes: Node[];
};

// groupNodes раскладывает узлы по секциям.
//
// Порядок секций: сначала «без группы», затем группы в порядке справочника
// (он приходит уже отсортированным по sort_order, name — переупорядочивать его
// здесь значило бы дублировать серверное правило). Пустые секции не выводятся:
// группа, все узлы которой отфильтрованы, на экране не нужна.
//
// Узел, ссылающийся на неизвестную группу, попадает в секцию «без группы».
// Такое состояние достижимо гонкой: справочник ещё не приехал или группу
// удалили в соседней вкладке. Прятать из-за этого сам узел нельзя — узел важнее
// своей метки (§99.9).
export function groupNodes(nodes: Node[], groups: NodeGroup[]): NodeSection[] {
  const known = new Map(groups.map((g) => [g.id, g]));
  const ungrouped: Node[] = [];
  // Map сохраняет порядок вставки, но полагаться на него нельзя: узлы идут в
  // порядке сортировки, а не справочника. Порядок секций задаётся обходом
  // groups ниже.
  const byGroup = new Map<string, Node[]>();

  for (const n of nodes) {
    const id = n.group_id;
    if (!id || !known.has(id)) {
      ungrouped.push(n);
      continue;
    }
    const bucket = byGroup.get(id);
    if (bucket) bucket.push(n);
    else byGroup.set(id, [n]);
  }

  const sections: NodeSection[] = [];
  if (ungrouped.length > 0) sections.push({ group: null, nodes: ungrouped });
  for (const g of groups) {
    const items = byGroup.get(g.id);
    if (items && items.length > 0) sections.push({ group: g, nodes: items });
  }
  return sections;
}

// usedGroups — группы, которые реально встречаются у переданных узлов, в порядке
// справочника.
//
// Из них строится фильтр «Группа» на экране «Узлы» (§99.5). Полный справочник
// там не годится: он глобальный, и в команде, где заведены три группы из
// десяти, семь оставшихся пунктов гарантированно дают пустой список — фильтр
// предлагал бы выбор, о котором заранее известно, что он ничего не покажет.
//
// Считать надо по узлам скоупа ДО клиентских фильтров (метод/статус/группа):
// иначе выбор группы схлопнул бы список опций до неё одной, и вернуться к
// «Все группы» через другую группу стало бы нельзя. В сквозном режиме §86 сюда
// приезжают узлы всех команд — и набор групп получается по всем командам сразу,
// без отдельной ветки кода.
export function usedGroups(nodes: Node[], groups: NodeGroup[]): NodeGroup[] {
  const used = new Set<string>();
  for (const n of nodes) {
    if (n.group_id) used.add(n.group_id);
  }
  return groups.filter((g) => used.has(g.id));
}

// hasUngroupedNodes — есть ли среди узлов хоть один без группы. Пункт «Без
// группы» показывается только тогда: когда все узлы разложены по группам, он
// такой же заведомо пустой выбор, как и неиспользуемая группа.
export function hasUngroupedNodes(nodes: Node[]): boolean {
  return nodes.some((n) => !n.group_id);
}

// hasGroupedSections — есть ли хоть одна секция с группой. Пока групп нет,
// экран рендерится плоским списком, как до §99: одинокая безымянная секция
// добавила бы разметку, ничего не сообщая.
export function hasGroupedSections(sections: NodeSection[]): boolean {
  return sections.some((s) => s.group !== null);
}

// COLLAPSED_KEY — свёрнутые группы, per-browser (§99.5).
//
// localStorage, а не sessionStorage (где живут фильтры §54.3): свёрнутая группа
// — настройка рабочего места, а не состояние сиюминутной задачи, и обязана
// пережить перезапуск браузера. Ключ соседствует с nexus.overview.view.
const COLLAPSED_KEY = "nexus.overview.groups.collapsed";

// loadCollapsed — множество id свёрнутых групп. Мусор и недоступное хранилище
// (приватный режим) дают пустое множество: развёрнутый список — безопасный
// дефолт, свёрнутый по ошибке выглядел бы как пропавшие узлы.
export function loadCollapsed(): Set<string> {
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY);
    if (!raw) return new Set();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((x): x is string => typeof x === "string"));
  } catch {
    return new Set();
  }
}

export function saveCollapsed(ids: Set<string>): void {
  try {
    if (ids.size === 0) window.localStorage.removeItem(COLLAPSED_KEY);
    else window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...ids]));
  } catch {
    // приватный режим / переполнение — сворачивание просто не переживёт сессию
  }
}
