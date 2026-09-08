import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

// Репо-широкие стражи двух правил, которые легко нарушить копипастой и трудно
// заметить глазами.

const SRC = join(process.cwd(), "src");

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) sources(p, out);
    else if (/\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

describe("вложенная интерактивность", () => {
  // <button> внутри <a> — невалидный HTML, и вложенная кнопка перехватывает
  // клик у ссылки. Там, где действие это переход, нужна ссылка со стилями
  // кнопки: buttonClasses() в components/ui/Button.tsx.
  it("нет <Button> внутри <Link> или <a>", () => {
    const bad: string[] = [];
    for (const file of sources(SRC)) {
      if (file.endsWith(".test.tsx") || file.endsWith(".test.ts")) continue;
      const text = readFileSync(file, "utf8");
      // Ссылка, внутри которой на ближайших строках открывается <Button.
      const re = /<(?:Link|a)\b[^>]*>[\s\S]{0,200}?<Button\b/g;
      if (re.test(text)) bad.push(file.replace(SRC, "src"));
    }
    expect(bad).toEqual([]);
  });
});

describe("растянутая ссылка", () => {
  // Классы приёма живут в одном месте (lib/stretchedLink.ts). Копия в компоненте
  // разъедется с ним молча: контракт из трёх участников, и забытый третий —
  // это немой спарклайн или пропавшая подсказка.
  it("класс растяжения не набирается руками мимо lib/stretchedLink.ts", () => {
    // Ищем по частям, чтобы сам страж не считался нарушителем: цельный литерал
    // в этом файле поймал бы его собственный текст.
    const needle = ["after:", "inset-0"].join("");
    const bad: string[] = [];
    for (const file of sources(SRC)) {
      if (file.endsWith(join("lib", "stretchedLink.ts"))) continue;
      if (file.endsWith(".test.ts") || file.endsWith(".test.tsx")) continue;
      if (readFileSync(file, "utf8").includes(needle)) bad.push(file.replace(SRC, "src"));
    }
    expect(bad).toEqual([]);
  });
});
