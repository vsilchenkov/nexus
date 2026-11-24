#!/usr/bin/env node
// Открывает Nexus в браузере, как только Web под отладчиком начнёт отвечать.
//
// Запускается фоновой задачей VS Code «web: open browser when ready» — она стоит
// preLaunchTask'ом у конфигурации «Web (debug)» (и, значит, у «Nexus: all»).
// Задача сразу печатает строку «waiting», VS Code по ней считает её готовой и
// стартует отладку, а скрипт тем временем опрашивает Web.
//
// Почему не serverReadyAction по строке лога «web listening»: уровень логов
// меняется на лету из настроек в БД (§51.9), и при уровне warn/error этой строки
// в выводе нет вовсе — браузер молча не открылся бы. Опрос HTTP от уровня логов
// не зависит.
//
// Готовность — ответ Nexus на /api/version (200 и поле version), а не любой ответ
// на порту: порт может быть занят чужим сервисом, и открывать его незачем.
//
// Адрес — из web.http_addr файла config/config_debug.yml (как у самого Web);
// переопределение — NEXUS_WEB_URL. Отключить открытие — NEXUS_NO_BROWSER=1.
// Сколько ждать — NEXUS_OPEN_BROWSER_TIMEOUT_SEC (по умолчанию 300: холодная
// сборка под отладчиком бывает долгой). --dry-run — только напечатать команду.

import { readFileSync } from "node:fs";
import { spawn } from "node:child_process";

const PREFIX = "nexus-open-browser:";
const CONFIG = "config/config_debug.yml";
const dryRun = process.argv.includes("--dry-run");
const timeoutSec = Number(process.env.NEXUS_OPEN_BROWSER_TIMEOUT_SEC) || 300;

// webAddr — значение web.http_addr из конфига; без файла или ключа — ":8000".
function webAddr() {
  let section = "";
  try {
    for (const line of readFileSync(CONFIG, "utf8").split(/\r?\n/)) {
      const top = line.match(/^([A-Za-z_][\w-]*):/);
      if (top) {
        section = top[1];
        continue;
      }
      const m = section === "web" && line.match(/^\s+http_addr:\s*["']?([^"'\s#]+)/);
      if (m) return m[1];
    }
  } catch {
    // Нет конфига (запуск не из корня репозитория) — адрес по умолчанию.
  }
  return ":8000";
}

// webURL — адрес, который открывать в браузере.
function webURL() {
  if (process.env.NEXUS_WEB_URL) return process.env.NEXUS_WEB_URL.replace(/\/+$/, "");
  const addr = webAddr();
  const i = addr.lastIndexOf(":");
  let host = i > 0 ? addr.slice(0, i) : "";
  if (host === "" || host === "0.0.0.0" || host === "[::]") host = "localhost";
  return `http://${host}:${addr.slice(i + 1)}`;
}

// isNexus — отвечает ли по адресу именно Nexus.
async function isNexus(url) {
  try {
    const r = await fetch(`${url}/api/version`, { signal: AbortSignal.timeout(2000) });
    if (!r.ok) return false;
    const body = await r.json();
    return typeof body?.version === "string";
  } catch {
    return false;
  }
}

function openBrowser(url) {
  const [cmd, args] =
    process.platform === "win32"
      ? ["cmd", ["/c", "start", "", url]]
      : process.platform === "darwin"
        ? ["open", [url]]
        : ["xdg-open", [url]];
  if (dryRun) {
    console.log(`${PREFIX} dry-run: ${cmd} ${args.join(" ")}`);
    return;
  }
  spawn(cmd, args, { detached: true, stdio: "ignore", windowsHide: true }).unref();
}

async function main() {
  // Строка «waiting» печатается ВСЕГДА и первой: по ней VS Code считает фоновую
  // задачу готовой и запускает отладку (problemMatcher в tasks.json).
  if (process.env.NEXUS_NO_BROWSER === "1") {
    console.log(`${PREFIX} waiting skipped (NEXUS_NO_BROWSER=1)`);
    return;
  }
  const url = webURL();
  console.log(`${PREFIX} waiting for ${url} (up to ${timeoutSec}s)`);
  const deadline = Date.now() + timeoutSec * 1000;
  while (Date.now() < deadline) {
    if (await isNexus(url)) {
      openBrowser(url);
      console.log(`${PREFIX} opened ${url}`);
      return;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  console.log(`${PREFIX} gave up: ${url}/api/version did not answer as Nexus in ${timeoutSec}s`);
}

await main();
