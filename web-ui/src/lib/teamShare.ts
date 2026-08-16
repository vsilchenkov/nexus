import { useEffect, useRef, useState } from "react";
import { useLocation, useSearchParams } from "react-router-dom";

import { TEAM_SCOPE_ALL, setTeamScopeAll, useAllTeamsScope } from "./teamScope";
import { useMyTeams, useSwitchTeam } from "./teams";

// Слой «Ссылка на команду» (§76). Активная команда живёт только в серверной
// сессии (§4.15), поэтому адрес страницы не описывал, в какой команде она
// открыта: переслать коллеге «рабочий стол команды X» было нечем. Здесь:
// (1) параметр `?team=<slug>` — зеркало текущей команды сессии в адресной
// строке, (2) одноразовое применение параметра из входящей ссылки.
//
// Резолв slug → team_id целиком клиентский, по членствам из GET /api/me/teams:
// нового эндпоинта не нужно, а «нет такой команды» и «я не член» неотличимы
// by design (no-leak, как единый 404 резолвера узла в §58).

// TEAM_PARAM — имя query-параметра со slug'ом команды.
export const TEAM_PARAM = "team";

// teamPageUrl — абсолютная ссылка на рабочее пространство команды (§76.2) для
// кнопки «Поделиться». Путь всегда «/» (рабочий стол), а не текущая страница:
// кнопка шарит ЛЮБУЮ строку списка команд, а «текущая страница чужой команды»
// смысла не имеет (на /nodes/:id команду задаёт сам узел — §58, /audit закрыт
// для viewer). Фильтры и период рабочего стола (§54/§71) — личное состояние
// экрана и в шаренную ссылку не попадают. Приём тот же, что nodePageUrl (§58).
//
// Slug валидируется бэкендом как ^[a-z][a-z0-9_]{0,31}$ (domain/team.go) и
// неизменяем (PUT /api/teams принимает только name) — ссылка не протухает;
// encodeURIComponent тут страховка на случай ослабления паттерна.
export function teamPageUrl(slug: string): string {
  return `${window.location.origin}${teamPagePath(slug)}`;
}

// teamPagePath — ПУТЬ рабочего стола команды (§76.2), без origin. Отдельно от
// teamPageUrl, потому что у них разные потребители: <Link to=…> и href принимают
// путь (абсолютный URL react-router считает внешней навигацией), а кнопка
// «Поделиться» шлёт полный адрес. Формат параметра описан здесь ОДИН раз —
// иначе ссылка из буфера и ссылка из сайдбара однажды разойдутся, и разойдутся
// молча (§89.3).
export function teamPagePath(slug: string): string {
  return `/?${TEAM_PARAM}=${encodeURIComponent(slug)}`;
}

// TEAM_SCOPE_ALL_PATH — адрес сквозного режима «Все команды» (§86.5). Отдельная
// константа, а не teamPagePath(TEAM_SCOPE_ALL) на вызывающей стороне: `*` — не
// slug команды, и подстановка его в «путь команды» читалась бы как ошибка.
// Экранирование не нужно и не происходит: encodeURIComponent символ `*` не
// трогает (он незарезервирован), так что в адресе окажется ровно `?team=*` —
// то, что разбирает useTeamUrlParam.
export const TEAM_SCOPE_ALL_PATH = `/?${TEAM_PARAM}=${TEAM_SCOPE_ALL}`;

// TEAM_PARAM_ROUTES — маршруты, на которых параметр имеет смысл. Белый список,
// а не чёрный: новый маршрут не должен молча получить параметр.
//
// Исключены осознанно:
//   /nodes/*    — командой страницы владеет сам узел (§58, useEnsureNodeTeam);
//                 два механизма переключения на одном экране подрались бы;
//   /settings/* — раздел вне скоупа команды (§7.14.1, TEAM_INDEPENDENT_KEYS);
//   /logs       — консоль служебных логов инстанса, к команде отношения не имеет.
// /kafka сам по себе team-нейтрален (кластерные метрики), но параметр там
// зеркалит команду шапки — скопированный адрес любой страницы открывается в той
// же команде.
const TEAM_PARAM_ROUTES = new Set(["/", "/kafka", "/audit"]);

// teamParamAllowed — живёт ли параметр `?team=` на этом маршруте (§76.3).
// Завершающие слэши срезаются: адрес правят руками, а `/audit/` — тот же экран,
// что `/audit` (react-router сюда его и приводит), и молча оставаться без
// ссылки он не должен.
export function teamParamAllowed(pathname: string): boolean {
  const normalized = pathname.replace(/\/+$/, "") || "/";
  return TEAM_PARAM_ROUTES.has(normalized);
}

// MAX_SLUG_LEN — потолок длины значения параметра. Домен ограничивает slug 32
// символами (`^[a-z][a-z0-9_]{0,31}$`), в query же может прийти что угодно, и
// это значение попадает в текст баннера: без потолка чужая ссылка с километровой
// строкой распирала бы страницу. Запас сверх домена — чтобы «слишком длинный, но
// похожий на slug» ввод всё же дошёл до баннера как ненайденная команда.
const MAX_SLUG_LEN = 64;

// normalizeSlug — значение параметра к каноническому виду: пусто/пробелы → null
// (`?team=` из недоделанной ссылки не должен показывать баннер с пустым именем),
// регистр вниз (slug'и всегда строчные, но `?team=Alpha` набирают руками),
// длина — под потолок.
function normalizeSlug(raw: string | null): string | null {
  return (raw ?? "").trim().toLowerCase().slice(0, MAX_SLUG_LEN) || null;
}

// TeamUrlState — состояние баннера «команда недоступна» (§76.5).
export type TeamUrlState = {
  // unavailableSlug — slug из ссылки, который не удалось применить (нет такой
  // команды, пользователь не её член, либо членство сняли — 403 на switch).
  unavailableSlug: string | null;
  dismiss: () => void;
};

// useTeamUrlParam — единственный писатель параметра `?team=` и применяющий его
// хук (§76.4). Инвариант на разрешённых маршрутах: значение параметра равно
// slug'у текущей команды сессии; расхождение живёт только внутри окна
// применения входящей ссылки.
//
// Почему зеркало, а не «применил и удалил»: параметр обязан быть постоянным —
// именно он и есть ссылка на команду. Мигающий параметр периодически врёт и не
// проверяется одним утверждением.
//
// Монтируется РОВНО ОДИН раз на приложение (components/TeamUrlSync.tsx в
// AppShell): ref-guard'ы обязаны переживать переходы между страницами, а два
// экземпляра дрались бы за адресную строку.
export function useTeamUrlParam(): TeamUrlState {
  const { pathname } = useLocation();
  const [params, setParams] = useSearchParams();
  const { data } = useMyTeams();
  const { mutate: switchTeam } = useSwitchTeam();

  const [unavailableSlug, setUnavailableSlug] = useState<string | null>(null);
  // pendingSlug — намеченная к записи в URL команда (см. writeParam): держится в
  // state, а не в ref, именно ради гарантированного рендера между решением и
  // записью.
  const [pendingSlug, setPendingSlug] = useState<string | null>(null);

  // handled — последнее разобранное значение параметра: переключаем РОВНО ОДИН
  // РАЗ на значение, пока оно не сменилось (приём handledKey из lib/nodeShare.ts,
  // §58). Страхует от повторного switch-team в окне между неудачей (403,
  // недоступная команда) и приведением параметра к текущей команде.
  //
  // Это одноразовость на значение, а не «навсегда»: явный переход по той же
  // ссылке (клик в мессенджере, ввод адреса) переключит команду снова —
  // осознанное действие пользователя. Драки с ручным переключением в шапке при
  // этом нет: зеркало сразу переписывает параметр на выбранную команду, и
  // применять становится нечего.
  const handled = useRef<string | null>(null);
  // awaiting — наш switch-team в полёте. Пока членства не перечитаны, зеркало
  // заморожено: иначе оно записало бы в URL прежнюю команду и затёрло саму
  // ссылку, которую сейчас применяет.
  const awaiting = useRef<string | null>(null);

  const allTeams = useAllTeamsScope();

  const allowed = teamParamAllowed(pathname);
  const desired = allowed ? normalizeSlug(params.get(TEAM_PARAM)) : null;
  const currentSlug = data?.items.find((m) => m.id === data.current_team_id)?.slug ?? "";

  useEffect(() => {
    // Членства ещё не разрешились (или текущая команда сессии в них не найдена —
    // окно самолечения §44.H) — решать не по чему. Аналог isFetchedAfterMount
    // из §58 здесь НЕ применим: у ["me-teams"] нет refetchOnMount:"always", и при
    // тёплом кеше (staleTime 30с) флаг остался бы false навсегда — хук не сработал
    // бы вообще. Цена: при протухшем current_team_id (команду сменили в другой
    // вкладке) решение принимается по кешу; refetch по фокусу окна приводит
    // зеркало к правде сам.
    if (!allowed || !data || currentSlug === "") return;

    // writeParam — привести параметр к переданному slug'у. Функциональная форма
    // обязательна: чужие ключи (фильтры рабочего стола §54) обязаны выжить.
    // replace, а не push — переключение команды не должно засорять историю.
    //
    // Запись идёт в ДВА прохода, и это не перестраховка, а лечение гонки, которую
    // поймал стенд (§76.7). `setSearchParams(prev => …)` из react-router отдаёт в
    // `prev` НЕ актуальную query-строку, а снимок своего рендера. Смена команды
    // будит сразу двух писателей в одном коммите: нас и рабочий стол (§71 сбрасывает
    // период). Тот пишет вторым и своим устаревшим снимком возвращает прежний
    // `?team=`, причём итоговая строка совпадает с исходной — location не меняется,
    // повторного рендера нет, и ссылка молча остаётся врать («переключил команду в
    // шапке — параметр от прежней»).
    //
    // Поэтому сначала запоминаем намерение в state: он гарантирует новый рендер
    // ПОСЛЕ всех записей коммита, и уже на нём `params`/`prev` актуальны. Ждать
    // изменения location для этого нельзя — его как раз может и не быть.
    const writeParam = (slug: string) => {
      if (params.get(TEAM_PARAM) === slug) {
        if (pendingSlug !== null) setPendingSlug(null);
        return; // идемпотентность: иначе цикл эффектов
      }
      if (pendingSlug !== slug) {
        setPendingSlug(slug); // проход 1: намерение, запись — следующим рендером
        return;
      }
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.set(TEAM_PARAM, slug);
          return next;
        },
        { replace: true },
      );
    };

    if (awaiting.current !== null) {
      if (awaiting.current === currentSlug) {
        awaiting.current = null; // сессия догнала ссылку — размораживаем зеркало
      } else if (awaiting.current === desired) {
        return; // переезд ещё в полёте
      } else {
        awaiting.current = null; // параметр сменился под нами — ожидание неактуально
      }
    }

    // §86.7.1: сквозной режим разбирается ДО резолва slug'а и выпадает из
    // машины зеркала целиком — он не команда, переключать нечего.
    //
    // Порядок веток здесь значим: без этой проверки `*` пошёл бы в поиск по
    // членствам, не нашёлся бы и поднял баннер «команда недоступна», а параметр
    // тут же переписался бы на текущую команду — режим не включился бы ни разу.
    // Применяется РОВНО ОДИН РАЗ на значение — та же одноразовость, что у
    // slug'а команды. Без неё выбор команды из сквозного режима не работал бы:
    // `setTeamScopeAll(false)` будит этот же эффект, в URL всё ещё `*`, и режим
    // включался бы обратно — переключение выглядело бы как «не реагирует».
    // После разбора решение принимает текущий режим (ветки ниже).
    if (desired === TEAM_SCOPE_ALL && handled.current !== desired) {
      handled.current = desired;
      setUnavailableSlug(null);
      setTeamScopeAll(true); // входящая ссылка `/?team=*` включает режим
      return;
    }
    // Режим включён (переключателем или ссылкой), а параметра нет — например
    // после перехода «Узлы» в сайдбаре, открывающего `/` без query. Возвращаем
    // параметр, иначе режим молча схлопнулся бы в команду сессии.
    if (allTeams) {
      writeParam(TEAM_SCOPE_ALL);
      return;
    }
    if (desired === null) {
      writeParam(currentSlug);
      return;
    }
    if (desired === currentSlug) {
      handled.current = desired;
      writeParam(currentSlug); // нормализация регистра (?team=Alpha → alpha)
      return;
    }
    if (handled.current === desired) {
      writeParam(currentSlug); // значение уже разбирали — держим в URL правду
      return;
    }
    handled.current = desired;

    const target = data.items.find((m) => m.slug.toLowerCase() === desired);
    if (!target) {
      setUnavailableSlug(desired);
      writeParam(currentSlug);
      return;
    }

    setUnavailableSlug(null);
    awaiting.current = target.slug;
    switchTeam(target.id, {
      // 403: членство сняли между копированием ссылки и её открытием. Членства
      // перечитает сам useSwitchTeam — нам остаётся разморозить зеркало (иначе
      // параметр залипнет на недостижимой команде) и показать баннер.
      onError: () => {
        awaiting.current = null;
        setUnavailableSlug(target.slug);
      },
    });
    // unavailableSlug в зависимостях не для чтения, а ради перезапуска эффекта
    // после неудачи: на 403 внутри onError меняется только он. Данные членств
    // при этом остаются той же ссылкой (structural sharing react-query), и без
    // этой зависимости зеркало залипло бы на недостижимой команде до следующей
    // навигации.
  }, [
    allowed,
    allTeams,
    data,
    currentSlug,
    desired,
    params,
    setParams,
    switchTeam,
    unavailableSlug,
    pendingSlug,
  ]);

  return { unavailableSlug, dismiss: () => setUnavailableSlug(null) };
}
