// Сниппет «совпадение в комментарии» (§103.4).
//
// Сервер ищет подстроку ILIKE-ом в path, target_url и comment. Узел, попавший в
// выдачу ТОЛЬКО благодаря комментарию, без подсказки выглядел бы случайным —
// поэтому клиент показывает кусок комментария вокруг совпадения.
//
// Сравнение обязано совпадать с ILIKE PostgreSQL, а тот сворачивает регистр
// ПОСИМВОЛЬНО (lower() через towlower). Отсюда свёртка по кодпоинтам:
//   - toLowerCase всей строки дал бы контекстные формы (греческая финальная
//     сигма), которых у посимвольного lower() нет;
//   - toLocaleLowerCase в турецкой локали превращает I в ı;
//   - длина строки после свёртки может измениться («İ» → «i̇», две единицы
//     UTF-16), поэтому индекс в свёрнутой строке без карты смещений указывал бы
//     не на тот символ исходной.
// Там, где правила JS и PG всё же разойдутся, функция возвращает null: узел в
// выдаче остаётся, пропадает только подсказка.

// SNIPPET_CONTEXT — сколько символов (кодпоинтов) контекста с каждой стороны.
export const SNIPPET_CONTEXT = 40;

const ELLIPSIS = "…";

export type CommentSnippet = {
  before: string; // «…» уже вклеено, если текст слева обрезан
  match: string; // совпадение в регистре исходного текста
  after: string; // «…» уже вклеено, если текст справа обрезан
};

type Folded = {
  text: string; // свёрнутая строка
  start: number[]; // для каждой единицы UTF-16 свёрнутой строки — начало кодпоинта в исходной
  end: number[]; // … и его конец (исключительно)
};

function fold(s: string): Folded {
  let text = "";
  const start: number[] = [];
  const end: number[] = [];
  let i = 0;
  for (const ch of s) {
    const lo = ch.toLowerCase();
    for (let k = 0; k < lo.length; k++) {
      start.push(i);
      end.push(i + ch.length);
    }
    text += lo;
    i += ch.length;
  }
  return { text, start, end };
}

// containsFolded — есть ли query в text без учёта регистра (по правилам fold).
export function containsFolded(text: string, query: string): boolean {
  if (!query) return false;
  return fold(text).text.includes(fold(query).text);
}

// oneLine — переводы строк и табуляции схлопываются в пробел: сниппет живёт в
// одной строке, а искать надо в исходном тексте (как ILIKE).
function oneLine(s: string): string {
  return s.replace(/\s+/g, " ");
}

// commentSnippet — окно комментария вокруг ПЕРВОГО совпадения или null, если
// совпадения нет (или пуст запрос/комментарий).
export function commentSnippet(comment: string, query: string): CommentSnippet | null {
  if (!comment || !query) return null;
  const src = fold(comment);
  const q = fold(query).text;
  const at = src.text.indexOf(q);
  if (at < 0) return null;
  const from = src.start[at];
  const to = src.end[at + q.length - 1];

  // Окно — в кодпоинтах, чтобы не разрезать суррогатную пару (emoji).
  const left = Array.from(comment.slice(0, from));
  const right = Array.from(comment.slice(to));
  const cutLeft = left.length > SNIPPET_CONTEXT;
  const cutRight = right.length > SNIPPET_CONTEXT;
  let before = oneLine(left.slice(-SNIPPET_CONTEXT).join("")).trimStart();
  let after = oneLine(right.slice(0, SNIPPET_CONTEXT).join("")).trimEnd();
  if (cutLeft) before = ELLIPSIS + before;
  if (cutRight) after = after + ELLIPSIS;
  return { before, match: oneLine(comment.slice(from, to)), after };
}

// SnippetSource — поля узла, по которым сервер ищет (§103.2).
export type SnippetSource = { path: string; target_url?: string; comment?: string };

// commentOnlyHit — сниппет, если узел найден ТОЛЬКО по комментарию (§103.4):
// при совпадении в пути или адресе подсказка лишняя. query — ровно та строка,
// что ушла на сервер, а не текущий текст поля.
export function commentOnlyHit(n: SnippetSource, query: string): CommentSnippet | null {
  if (!query || !n.comment) return null;
  if (containsFolded(n.path, query) || containsFolded(n.target_url ?? "", query)) return null;
  return commentSnippet(n.comment, query);
}
