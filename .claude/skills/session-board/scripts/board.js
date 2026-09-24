#!/usr/bin/env node
// Доска параллельных сессий Claude Code в одном репозитории.
//
// Порт идеи session-board из vika, но без привязки к ИБ 1С: доска ключуется
// по основному git-каталогу репозитория, поэтому её видят все сессии этого
// репозитория, включая сессии в git worktree.
//
// Режимы:
//   node board.js hook                 — обработчик хуков (JSON события в stdin)
//   node board.js show [--tail N]      — кто работает, над чем, объявления, журнал
//   node board.js say <текст> [--hours N]
//   node board.js task <текст>         — задача сессии (пусто — снять)
//   node board.js clear [--all]        — снять свои объявления (или все)
// Сессию CLI берёт из CLAUDE_CODE_SESSION_ID (есть в Bash-туле) или --as <id>.
//
// Данные лежат вне репозитория: %LOCALAPPDATA%\claude-session-board\<ключ>\.
// Хук никогда не падает и не блокирует работу, кроме одного случая: опасная
// git-операция (смена ветки, stash, reset, add -A…) при живой соседней сессии
// в том же рабочем дереве — тогда вместо молчаливого выполнения спрашиваем
// пользователя (урок 24.09.2026: git switch -c увёл HEAD соседней сессии).
"use strict";

const fs = require("fs");
const path = require("path");
const os = require("os");
const crypto = require("crypto");

const MIN = 60 * 1000;
const ACTIVE_MS = 30 * MIN; // сессия «активна», если действовала за это время
const VISIBLE_MS = 6 * 60 * MIN; // дольше молчит — в подсказки не попадает
const FILE_CONFLICT_MS = 60 * MIN; // чужая правка файла считается свежей
const KEEP_SESSION_MS = 7 * 24 * 60 * MIN;
const LOG_LIMIT = 1024 * 1024;
const MAX_FILES = 20;
const MAX_CMDS = 5;

// ---------- утилиты ----------

function now() {
  return Date.now();
}

function hhmm(ms) {
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getDate())}.${p(d.getMonth() + 1)} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function ago(ms) {
  const m = Math.round((now() - ms) / MIN);
  if (m < 1) return "только что";
  if (m < 60) return `${m} мин назад`;
  const h = Math.floor(m / 60);
  return `${h} ч ${m % 60} мин назад`;
}

function short(id) {
  return String(id || "").slice(0, 8);
}

function oneLine(text, max) {
  const s = String(text || "").replace(/\s+/g, " ").trim();
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}

// Доска лежит на диске пользователя, но в неё попадают куски запросов и
// команд — секреты вырезаем до записи.
function maskSecrets(text) {
  return String(text || "")
    .replace(/(bot\d+):[A-Za-z0-9_-]{20,}/g, "$1:***")
    .replace(/\bsntry[a-z]?_[0-9a-zA-Z]+/g, "sntry_***")
    .replace(/\b(glpat|ghp|gho|github_pat|xox[abp])[-_][A-Za-z0-9_-]{10,}/g, "$1-***")
    .replace(/(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/gi, "$1 ***")
    .replace(/(\/\/[^/\s:@]+):[^@\s/]+@/g, "$1:***@")
    .replace(/\b(password|passwd|pwd|token|secret|api[_-]?key|PGPASSWORD)(\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)/gi, "$1$2***")
    .replace(/[A-Za-z0-9+/_-]{40,}={0,2}/g, "***");
}

function readJSON(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return fallback;
  }
}

function writeJSON(file, data) {
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(data, null, 1));
  fs.renameSync(tmp, file);
}

function readLines(file) {
  try {
    return fs.readFileSync(file, "utf8").split("\n").filter(Boolean);
  } catch {
    return [];
  }
}

// ---------- git без запуска git ----------

// Корень рабочего дерева и основной git-каталог. Worktree держит в .git файл
// «gitdir: <repo>/.git/worktrees/<имя>» — доску ключуем по основному каталогу,
// чтобы сессии всех worktree одного репозитория видели друг друга.
function findRepo(start) {
  let dir = path.resolve(start || process.cwd());
  for (;;) {
    const dotgit = path.join(dir, ".git");
    let st = null;
    try {
      st = fs.statSync(dotgit);
    } catch {}
    if (st && st.isDirectory()) return { root: dir, gitDir: dotgit, commonDir: dotgit };
    if (st && st.isFile()) {
      const m = /gitdir:\s*(.+)/.exec(fs.readFileSync(dotgit, "utf8"));
      if (m) {
        const gitDir = path.resolve(dir, m[1].trim());
        let commonDir = gitDir;
        try {
          commonDir = path.resolve(gitDir, fs.readFileSync(path.join(gitDir, "commondir"), "utf8").trim());
        } catch {}
        return { root: dir, gitDir, commonDir };
      }
    }
    const up = path.dirname(dir);
    if (up === dir) return { root: path.resolve(start || process.cwd()), gitDir: null, commonDir: null };
    dir = up;
  }
}

function currentBranch(repo) {
  if (!repo.gitDir) return "";
  try {
    const head = fs.readFileSync(path.join(repo.gitDir, "HEAD"), "utf8").trim();
    const m = /^ref:\s*refs\/heads\/(.+)$/.exec(head);
    return m ? m[1] : `(detached ${head.slice(0, 7)})`;
  } catch {
    return "";
  }
}

function normPath(p) {
  const r = path.resolve(p);
  return process.platform === "win32" ? r.toLowerCase() : r;
}

// ---------- хранилище доски ----------

function boardDir(repo) {
  const base = process.env.SESSION_BOARD_DIR || path.join(process.env.LOCALAPPDATA || path.join(os.homedir(), ".cache"), "claude-session-board");
  const key = normPath(repo.commonDir ? path.dirname(repo.commonDir) : repo.root);
  const slug = key.replace(/[:\\/_. ]+/g, "-").replace(/^-+|-+$/g, "").slice(-60);
  const hash = crypto.createHash("sha1").update(key).digest("hex").slice(0, 6);
  const dir = path.join(base, `${slug}-${hash}`);
  fs.mkdirSync(path.join(dir, "sessions"), { recursive: true });
  return dir;
}

class Board {
  constructor(repo) {
    this.repo = repo;
    this.dir = boardDir(repo);
  }

  sessionFile(id) {
    return path.join(this.dir, "sessions", `${id.replace(/[^A-Za-z0-9_-]/g, "_")}.json`);
  }

  loadSession(id) {
    return readJSON(this.sessionFile(id), null);
  }

  saveSession(s) {
    writeJSON(this.sessionFile(s.id), s);
  }

  sessions() {
    let names = [];
    try {
      names = fs.readdirSync(path.join(this.dir, "sessions"));
    } catch {}
    return names
      .filter((n) => n.endsWith(".json"))
      .map((n) => readJSON(path.join(this.dir, "sessions", n), null))
      .filter(Boolean);
  }

  // Свежие соседи: не завершены и действовали недавно.
  others(selfId, withinMs) {
    const t = now();
    return this.sessions()
      .filter((s) => s.id !== selfId && !s.ended && t - (s.last || 0) < withinMs)
      .sort((a, b) => b.last - a.last);
  }

  cleanup() {
    const t = now();
    for (const s of this.sessions()) {
      const stale = t - (s.last || 0) > KEEP_SESSION_MS || (s.ended && t - s.ended > 24 * 60 * MIN);
      if (stale) {
        try {
          fs.unlinkSync(this.sessionFile(s.id));
        } catch {}
      }
    }
  }

  log(sessionId, type, value) {
    const file = path.join(this.dir, "events.jsonl");
    try {
      if (fs.statSync(file).size > LOG_LIMIT) fs.renameSync(file, path.join(this.dir, "events.1.jsonl"));
    } catch {}
    const rec = { t: now(), s: short(sessionId), e: type, v: value };
    fs.appendFileSync(file, JSON.stringify(rec) + "\n");
  }

  events(tail) {
    const lines = [...readLines(path.join(this.dir, "events.1.jsonl")), ...readLines(path.join(this.dir, "events.jsonl"))];
    return lines
      .slice(-tail)
      .map((l) => {
        try {
          return JSON.parse(l);
        } catch {
          return null;
        }
      })
      .filter(Boolean);
  }

  notesFile() {
    return path.join(this.dir, "notes.jsonl");
  }

  notes() {
    const t = now();
    return readLines(this.notesFile())
      .map((l) => {
        try {
          return JSON.parse(l);
        } catch {
          return null;
        }
      })
      .filter((n) => n && n.until > t);
  }

  addNote(owner, text, hours) {
    const t = now();
    const rec = { t, until: t + hours * 60 * MIN, owner: short(owner), text: maskSecrets(oneLine(text, 500)) };
    fs.appendFileSync(this.notesFile(), JSON.stringify(rec) + "\n");
    this.log(owner, "note", rec.text);
  }

  clearNotes(owner) {
    const keep = this.notes().filter((n) => owner && n.owner !== short(owner));
    fs.writeFileSync(this.notesFile(), keep.map((n) => JSON.stringify(n) + "\n").join(""));
    return keep.length;
  }
}

// ---------- сессия ----------

function touchSession(board, id) {
  const t = now();
  let s = board.loadSession(id);
  if (!s) {
    s = { id, short: short(id), started: t, last: t, root: board.repo.root, branch: currentBranch(board.repo), task: "", prompt: "", files: [], cmds: [], ended: 0, seenNotes: t, seenDigest: "" };
  }
  s.last = t;
  s.ended = 0;
  s.root = board.repo.root;
  return s;
}

// Ветка рабочего дерева сменилась не нашей командой — значит, её сменил
// кто-то другой. Находим виновника по журналу.
function detectForeignBranchChange(board, s) {
  const branch = currentBranch(board.repo);
  if (!branch || !s.branch || branch === s.branch) {
    if (branch) s.branch = branch;
    return "";
  }
  const hit = board
    .events(400)
    .reverse()
    .find((e) => e.e === "branch" && e.s !== s.short && e.v && e.v.to === branch);
  const who = hit ? `её сменила сессия ${hit.s} в ${hhmm(hit.t)}` : "кто сменил, по доске не видно: вручную или сессией без хуков";
  const msg = `ВНИМАНИЕ: ветка рабочего дерева сменилась: ${s.branch} → ${branch}; ${who}. Твои следующие коммиты уйдут в ${branch}. Проверь git status и согласуй с пользователем, прежде чем коммитить.`;
  board.log(s.id, "branch-seen", { from: s.branch, to: branch });
  s.branch = branch;
  return msg;
}

function describeSession(o, selfRoot) {
  const state = now() - o.last < ACTIVE_MS ? "активна" : "простаивает";
  const tree = normPath(o.root || "") === normPath(selfRoot) ? "это же рабочее дерево" : `дерево ${o.root}`;
  const lines = [`• ${o.short} — ${state} (${ago(o.last)}), ветка ${o.branch || "?"}, ${tree}`];
  if (o.task) lines.push(`  задача: ${o.task}`);
  if (o.prompt) lines.push(`  последний запрос (${hhmm(o.promptAt || o.last)}): «${o.prompt}»`);
  const files = (o.files || []).slice(-6).reverse().map((f) => f.p);
  if (files.length) lines.push(`  правки: ${files.join(", ")}${o.files.length > 6 ? ` (+${o.files.length - 6})` : ""}`);
  const cmd = (o.cmds || []).slice(-1)[0];
  if (cmd) lines.push(`  последняя команда (${hhmm(cmd.t)}): ${cmd.c}`);
  return lines.join("\n");
}

function digestOf(others) {
  const src = others.map((o) => [o.id, o.task, o.prompt, o.branch, (o.files || []).slice(-3).map((f) => f.p).join(",")].join("|")).join("\n");
  return crypto.createHash("sha1").update(src).digest("hex").slice(0, 12);
}

// Сводка для контекста модели: соседи (если что-то изменилось) и новые объявления.
function contextSummary(board, s, force) {
  const parts = [];
  const others = board.others(s.id, VISIBLE_MS);
  const digest = digestOf(others);
  if (others.length && (force || digest !== s.seenDigest)) {
    parts.push(`[session-board] Параллельные сессии этого репозитория:\n${others.map((o) => describeSession(o, board.repo.root)).join("\n")}`);
  }
  s.seenDigest = digest;
  const fresh = board.notes().filter((n) => n.t > (s.seenNotes || 0) && n.owner !== s.short);
  if (fresh.length) {
    parts.push(`[session-board] Новые объявления:\n${fresh.map((n) => `[${hhmm(n.t)} ${n.owner}] ${n.text}`).join("\n")}`);
    s.seenNotes = Math.max(...fresh.map((n) => n.t));
  }
  return parts.join("\n\n");
}

// ---------- разбор git-команд ----------

function gitCalls(command) {
  const calls = [];
  for (const seg of String(command || "").split(/&&|\|\||;|\||\n/)) {
    const m = /(?:^|\s)git(?:\.exe)?\s+((?:-[Cc]\s+\S+\s+)*)(\S+)(.*)$/.exec(seg.trim());
    if (m) {
      const args = m[3].replace(/"[^"]*"|'[^']*'/g, "Q").trim().split(/\s+/).filter(Boolean);
      calls.push({ sub: m[2], args, text: oneLine(seg, 120) });
    }
  }
  return calls;
}

function has(args, ...flags) {
  return args.some((a) => flags.includes(a));
}

// Операции, которые меняют общий для сессий HEAD, индекс или рабочие файлы.
function riskyGit(call) {
  const { sub, args } = call;
  switch (sub) {
    case "switch":
    case "checkout":
      return args.length > 0 ? "меняет ветку/файлы общего рабочего дерева" : "";
    case "stash":
      return ["list", "show"].includes(args[0]) ? "" : "прячет или возвращает незакоммиченные правки — в том числе чужие";
    case "reset":
    case "rebase":
    case "merge":
    case "pull":
    case "cherry-pick":
    case "am":
      return "двигает HEAD общей ветки";
    case "restore":
      return has(args, "--staged") && !has(args, "--worktree", "-W") ? "" : "откатывает правки в рабочих файлах";
    case "clean":
      return "удаляет неотслеживаемые файлы — в том числе чужие";
    case "add":
      return has(args, "-A", "--all", ".", "-u", "--update", ":/") ? "добавит в индекс и чужие правки" : "";
    case "commit":
      if (has(args, "--amend")) return "переписывает последний коммит — он может быть чужим";
      return args.some((a) => a === "--all" || /^-[A-Za-z]*a[A-Za-z]*$/.test(a)) ? "закоммитит и чужие правки" : "";
    case "branch":
      return has(args, "-m", "-M", "--move", "-d", "-D", "--delete", "-f", "--force") ? "переименовывает/удаляет ветку, на которой может стоять соседняя сессия" : "";
    default:
      return "";
  }
}

// ---------- обработчики хуков ----------

function out(obj) {
  if (obj) process.stdout.write(JSON.stringify(obj));
}

function ctx(event, text, extra) {
  if (!text) return null;
  return Object.assign({ hookSpecificOutput: { hookEventName: event, additionalContext: text } }, extra || {});
}

function onSessionStart(board, input) {
  board.cleanup();
  const s = touchSession(board, input.session_id);
  s.branch = currentBranch(board.repo);
  board.log(s.id, "start", { source: input.source || "", branch: s.branch });
  const summary = contextSummary(board, s, true);
  board.saveSession(s);
  const others = board.others(s.id, ACTIVE_MS).filter((o) => normPath(o.root) === normPath(board.repo.root));
  const head = `[session-board] Id этой сессии на доске: ${s.short}. Посмотреть соседей и журнал: node .claude/skills/session-board/scripts/board.js show; объявить: … say "<текст>"; задать свою задачу: … task "<текст>".`;
  const extra = others.length ? { systemMessage: `session-board: в этом рабочем дереве работают ещё ${others.length} сесс.: ${others.map((o) => `${o.short}${o.task ? ` (${oneLine(o.task, 50)})` : ""}`).join(", ")}` } : null;
  return ctx("SessionStart", [head, summary].filter(Boolean).join("\n\n"), extra);
}

function onPrompt(board, input) {
  const s = touchSession(board, input.session_id);
  const warn = detectForeignBranchChange(board, s);
  const prompt = maskSecrets(oneLine(input.prompt, 200));
  if (prompt && !prompt.startsWith("<")) {
    s.prompt = prompt;
    s.promptAt = now();
    board.log(s.id, "prompt", prompt);
  }
  const summary = contextSummary(board, s, false);
  board.saveSession(s);
  const text = [warn, summary].filter(Boolean).join("\n\n");
  return ctx("UserPromptSubmit", text, warn ? { systemMessage: `session-board: ${warn}` } : null);
}

function onPreTool(board, input) {
  const s = touchSession(board, input.session_id);
  const warn = detectForeignBranchChange(board, s);
  const calls = gitCalls(input.tool_input && input.tool_input.command);
  const neighbours = board.others(s.id, ACTIVE_MS).filter((o) => normPath(o.root) === normPath(board.repo.root));
  board.saveSession(s);
  if (!neighbours.length) return warn ? ctx("PreToolUse", warn) : null;
  const who = neighbours.map((o) => `${o.short}${o.task ? ` (${oneLine(o.task, 60)})` : o.prompt ? ` («${oneLine(o.prompt, 60)}»)` : ""}, ${ago(o.last)}`).join("; ");
  for (const c of calls) {
    const why = riskyGit(c);
    if (why) {
      board.log(s.id, "risky", c.text);
      return {
        hookSpecificOutput: {
          hookEventName: "PreToolUse",
          permissionDecision: "ask",
          permissionDecisionReason: `session-board: «${c.text}» ${why}, а в этом же рабочем дереве работает другая сессия: ${who}. Выполнить всё равно?`,
        },
      };
    }
  }
  if (calls.some((c) => c.sub === "commit")) {
    const files = [...new Set(neighbours.flatMap((o) => (o.files || []).map((f) => f.p)))].slice(-8);
    const note = `[session-board] Коммит при живой соседней сессии (${who}). Перед коммитом сверь git diff --cached --name-only: в индексе должны быть только твои файлы.${files.length ? ` Соседняя сессия меняла: ${files.join(", ")}.` : ""}`;
    return ctx("PreToolUse", [warn, note].filter(Boolean).join("\n"));
  }
  return warn ? ctx("PreToolUse", warn) : null;
}

function relFile(board, file) {
  const rel = path.relative(board.repo.root, path.resolve(board.repo.root, file));
  return (rel.startsWith("..") ? file : rel).replace(/\\/g, "/");
}

function onEdit(board, input, s) {
  const ti = input.tool_input || {};
  const file = ti.file_path || ti.notebook_path;
  if (!file) return null;
  const p = relFile(board, file);
  s.files = (s.files || []).filter((f) => f.p !== p).concat({ p, t: now() }).slice(-MAX_FILES);
  board.log(s.id, "edit", p);
  const t = now();
  const clash = board
    .others(s.id, ACTIVE_MS)
    .filter((o) => (o.files || []).some((f) => f.p === p && t - f.t < FILE_CONFLICT_MS));
  if (!clash.length) return null;
  const who = clash.map((o) => `${o.short}${o.task ? ` (${oneLine(o.task, 60)})` : ""}`).join(", ");
  return `[session-board] Файл ${p} за последний час правила и соседняя сессия ${who}. Перечитай файл перед следующей правкой и не коммить чужие изменения.`;
}

const BRANCH_MOVERS = ["switch", "checkout", "rebase", "merge", "pull", "reset", "stash", "am", "cherry-pick", "branch"];

// PostToolUse приходит уже после команды: если в ней был git-вызов, способный
// сменить ветку, смена — наша (пишем в журнал, чтобы соседи знали виновника).
// Если такого вызова не было, ветку сменил кто-то другой: чужие смены до нашей
// команды ловит ещё PreToolUse.
function onCommand(board, input, s) {
  const raw = input.tool_input && input.tool_input.command;
  const cmd = maskSecrets(oneLine(raw, 160));
  if (!cmd) return "";
  s.cmds = (s.cmds || []).concat({ c: cmd, t: now() }).slice(-MAX_CMDS);
  board.log(s.id, "cmd", cmd);
  const branch = currentBranch(board.repo);
  if (!branch || !s.branch || branch === s.branch) {
    if (branch) s.branch = branch;
    return "";
  }
  if (!gitCalls(raw).some((c) => BRANCH_MOVERS.includes(c.sub))) return detectForeignBranchChange(board, s);
  board.log(s.id, "branch", { from: s.branch, to: branch });
  s.branch = branch;
  return "";
}

function onPostTool(board, input) {
  const s = touchSession(board, input.session_id);
  const tool = input.tool_name || "";
  const note = tool === "Bash" || tool === "PowerShell" ? onCommand(board, input, s) : [detectForeignBranchChange(board, s), onEdit(board, input, s)].filter(Boolean).join("\n");
  board.saveSession(s);
  return ctx("PostToolUse", note);
}

function onSessionEnd(board, input) {
  const s = board.loadSession(input.session_id);
  if (!s) return null;
  s.ended = now();
  board.log(s.id, "end", input.reason || "");
  board.saveSession(s);
  return null;
}

const HANDLERS = {
  SessionStart: onSessionStart,
  UserPromptSubmit: onPrompt,
  PreToolUse: onPreTool,
  PostToolUse: onPostTool,
  SessionEnd: onSessionEnd,
};

function runHook() {
  let input = {};
  try {
    input = JSON.parse(fs.readFileSync(0, "utf8") || "{}");
  } catch {
    return;
  }
  const handler = HANDLERS[input.hook_event_name];
  if (!handler || !input.session_id) return;
  const board = new Board(findRepo(input.cwd || process.env.CLAUDE_PROJECT_DIR));
  try {
    out(handler(board, input));
  } catch (err) {
    try {
      fs.appendFileSync(path.join(board.dir, "errors.log"), `${new Date().toISOString()} ${input.hook_event_name}: ${err && err.stack}\n`);
    } catch {}
  }
}

// ---------- CLI ----------

function parseArgs(argv) {
  const opts = { _: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--all") opts.all = true;
    else if (a === "--as" || a === "--hours" || a === "--tail") opts[a.slice(2)] = argv[++i];
    else opts._.push(a);
  }
  return opts;
}

function resolveSelf(board, opts) {
  const want = opts.as || process.env.CLAUDE_CODE_SESSION_ID || "";
  if (!want) return null;
  return board.sessions().find((s) => s.id === want || s.short === short(want)) || { id: want, short: short(want) };
}

function formatEvent(e) {
  let v = e.v;
  if (e.e === "branch" || e.e === "branch-seen") v = `${e.v.from} → ${e.v.to}`;
  else if (e.e === "start") v = `${e.v.source || "?"}, ветка ${e.v.branch || "?"}`;
  else if (typeof v !== "string") v = JSON.stringify(v);
  const label = { start: "старт", end: "конец", prompt: "запрос", edit: "правка", cmd: "команда", note: "объявление", task: "задача", branch: "СМЕНА ВЕТКИ", "branch-seen": "увидела смену ветки", risky: "опасная git-операция" }[e.e] || e.e;
  return `${hhmm(e.t)} ${e.s} ${label}: ${oneLine(v, 150)}`;
}

function cmdShow(board, opts) {
  const self = resolveSelf(board, opts);
  const t = now();
  const list = board
    .sessions()
    .filter((s) => opts.all || t - s.last < 24 * 60 * MIN)
    .sort((a, b) => b.last - a.last);
  console.log(`Доска: ${board.dir}`);
  console.log(`\n== Сессии (${list.length}) ==`);
  for (const s of list) {
    const mark = self && s.id === self.id ? "  ← эта сессия" : "";
    const state = s.ended ? `завершена ${hhmm(s.ended)}` : t - s.last < ACTIVE_MS ? "активна" : "простаивает";
    const block = describeSession(s, board.repo.root).split("\n");
    block[0] = block[0].replace(/— (активна|простаивает)/, `— ${state}`) + mark;
    console.log(block.join("\n"));
  }
  const notes = board.notes();
  console.log(`\n== Объявления (${notes.length}) ==`);
  for (const n of notes) console.log(`[${hhmm(n.t)} ${n.owner}] ${n.text}  (до ${hhmm(n.until)})`);
  const tail = Number(opts.tail || 30);
  console.log(`\n== Журнал (последние ${tail}) ==`);
  for (const e of board.events(tail)) console.log(formatEvent(e));
}

function requireSelf(board, opts) {
  const self = resolveSelf(board, opts);
  if (!self) throw new Error("не знаю, от чьего имени писать: нет CLAUDE_CODE_SESSION_ID, укажите --as <id>");
  return self;
}

function cmdSay(board, opts) {
  const self = requireSelf(board, opts);
  const text = opts._.join(" ").trim();
  if (!text) throw new Error("пустое объявление");
  board.addNote(self.id, text, Number(opts.hours || 12));
  console.log(`Объявлено от ${short(self.id)}: ${text}`);
}

function cmdTask(board, opts) {
  const self = requireSelf(board, opts);
  const s = touchSession(board, self.id);
  s.task = maskSecrets(oneLine(opts._.join(" "), 160));
  board.log(s.id, "task", s.task || "(снята)");
  board.saveSession(s);
  console.log(s.task ? `Задача ${s.short}: ${s.task}` : `Задача ${s.short} снята`);
}

function cmdClear(board, opts) {
  const self = opts.all ? null : requireSelf(board, opts);
  const left = board.clearNotes(self && self.id);
  console.log(`Объявления сняты${self ? ` (${short(self.id)})` : " (все)"}; осталось: ${left}`);
}

function runCli(argv) {
  const [cmd, ...rest] = argv;
  const opts = parseArgs(rest);
  const board = new Board(findRepo(process.cwd()));
  const commands = { show: cmdShow, say: cmdSay, task: cmdTask, clear: cmdClear };
  const fn = commands[cmd || "show"];
  if (!fn) {
    console.error("Использование: board.js show [--tail N] [--all] | say <текст> [--hours N] | task <текст> | clear [--all] [--as <id>]");
    process.exitCode = 2;
    return;
  }
  try {
    fn(board, opts);
  } catch (err) {
    console.error(`board.js ${cmd}: ${err.message}`);
    process.exitCode = 1;
  }
}

if (process.argv[2] === "hook") {
  try {
    runHook();
  } catch {
    // Хук не имеет права ломать сессию.
  }
  process.exitCode = 0;
} else {
  runCli(process.argv.slice(2));
}

module.exports = { gitCalls, riskyGit, maskSecrets };
