import { nextThemeValue } from "./themes.mjs";
import { studyDay, dayOverview, shortName, groupedDayRows, groupedNearby, revisionSummary, readPreferences } from "./ux.mjs";
import {
  esc,
  clock,
  today,
  minutesNow,
  shift,
  monday,
  dateLabel,
  actual,
  items,
  statistics,
  intervals,
  freeTogether,
  ics,
  scheduleFocus,
  audienceName,
  withAudienceNames,
  dayRows,
  nowRow,
  placeLabel,
  sessionKind,
  lessonKind,
  subjectSummary,
  teacherWeekSummary,
  semesterOf,
  daysUntil,
  plural,
} from "./model.mjs";
import { nearbyLessons, nearbyBreaks, parseClassroom } from "./meetings.mjs";
import { botLinks, botLink } from "./links.mjs";
import { revisionRows } from "./revisions.mjs";
import { createData } from "./data.mjs";
import { initPlatform, backButton, share, install } from "./platform.mjs";

if (window.mpBoot) window.mpBoot.stage = "initializing";

const paths = {
  calendar:
    "M8 2v4m8-4v4M3 10h18M5 4h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2M7 14h2m6 0h2m-10 4h2",
  users:
    "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2m20 0v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75M13 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0",
  compare: "M3 7h17m-4-4 4 4-4 4M21 17H4m4-4-4 4 4 4",
  chart: "M4 3v18h17M8 16v-5m5 5V7m5 9v-9",
  settings:
    "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M12 2v3m0 14v3M2 12h3m14 0h3M5 5l2 2m10 10 2 2M5 19l2-2M17 7l2-2",
  left: "m14 6-6 6 6 6",
  right: "m10 6 6 6-6 6",
  down: "m6 9 6 6 6-6",
  up: "M7 17 17 7M7 7h10v10",
  clock: "M12 8v5l3 2M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
  pin: "M20 10c0 6-8 12-8 12S4 16 4 10a8 8 0 1 1 16 0M15 10a3 3 0 1 1-6 0 3 3 0 0 1 6 0",
  star: "m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9z",
  sun: "M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0",
  moon: "M20.5 13a8.5 8.5 0 0 1-9.5-10 9 9 0 1 0 9.5 10Z",
  system: "M3 3h18v13H3zM8 21h8m-4-5v5",
  search: "m21 21-5-5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
  close: "m6 6 12 12M6 18 18 6",
  download: "M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5",
  share: "M12 16V2m-5 5 5-5 5 5M5 11H3v11h18V11h-2",
  coffee:
    "M18 8h1a3 3 0 1 1 0 6h-1M3 8h15v7a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4V8M6 2v3m4-3v3m4-3v3M2 22h18",
  book: "M12 6v15M3 3c4-1 7 0 9 3 2-3 5-4 9-3v16c-4-1-7 0-9 2-2-2-5-3-9-2z",
  check: "m5 12 4 4L19 6",
  refresh: "M21 12a9 9 0 1 1-9-9c2.5 0 4.9 1 6.7 2.7L21 8M21 3v5h-5",
  wifi: "M2 8a16 16 0 0 1 20 0M5 12a11 11 0 0 1 14 0M8 16a6 6 0 0 1 8 0m-4 4h.01",
  phone:
    "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2m4 16h2",
  info: "M12 11v6m0-10h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
};
const icon = (n) =>
  `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="${paths[n] || paths.info}"/></svg>`;
const nav = [
  ["schedule", "calendar", "Моё расписание", "Расписание"],
  ["teachers", "users", "Преподаватели", "Преподаватели"],
  ["compare", "compare", "Встретимся", "Встретимся"],
  ["stats", "chart", "Статистика", "Статистика"],
  ["settings", "settings", "Настройки", "Настройки"],
];
const $ = (s) => document.querySelector(s);
let storage;
try {
  storage = localStorage;
} catch {
  storage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
}
// Настройки приезжают в теге страницы, а не отдельным запросом: раньше первая
// отрисовка ждала целый круг до сервера ради одного логического значения.
const config = (() => {
  try {
    return (
      JSON.parse($('meta[name="app-config"]')?.content || "{}") || {
        demo: false,
      }
    );
  } catch {
    return { demo: false };
  }
})();
let betaProfile = null;
let adminAccess = false;
if (!config.demo && config.admin_enabled) {
 try { adminAccess = (await fetch("/admin/access", {cache:"no-store", signal:AbortSignal.timeout(5000)})).ok; } catch {}
}
if (config.beta) {
  try { storage.removeItem("mp.schedule-cache.v1"); storage.removeItem("mp.changes.v1"); } catch {}
  const checkSession = async () => {
    const response = await fetch("/auth/me", {method:"POST", headers:{"Content-Type":"application/json"}, body:"{}", cache:"no-store", signal:AbortSignal.timeout(10000)});
    if (response.status === 401) { document.body.replaceChildren(); location.replace("/login"); throw new Error("Войдите через бота"); }
    if (!response.ok) throw new Error("Не удалось проверить вход. Обновите страницу.");
    return response.json();
  };
  betaProfile = await checkSession();
  document.addEventListener("visibilitychange", () => { if (!document.hidden) checkSession().catch(() => {}); });
  window.addEventListener("pageshow", event => { if(event.persisted) checkSession().catch(() => {}); });
}
// Неделя, вложенная сервером прямо в страницу. Расписание появляется вместе с
// разметкой, без круга до сервера. Это только первый кадр: обычный запрос всё
// равно уходит следом и приносит свежесть, подгруппы и правки.
const preload = (() => {
  try {
    const tag = $("#app-week");
    // Страница живёт в офлайн-кэше целиком, поэтому вложенная неделя может
    // оказаться вчерашней. Просроченную не берём: своя сохранённая копия хотя
    // бы честно подписана датой.
    if (!tag?.textContent || Date.now() / 1000 - Number(tag.dataset.at) > 120)
      return null;
    return JSON.parse(tag.textContent);
  } catch {
    return null;
  }
})();
let preloadUsed = false;
const prefStorage = config.demo
  ? {
      getItem: (k) => storage.getItem("demo." + k),
      setItem: (k, v) => storage.setItem("demo." + k, v),
    }
  : storage;
const pref = readPreferences(prefStorage, config.demo ? "" : document.cookie),
  query = new URLSearchParams(location.search);
const validID = (s) =>
  /^\d+$/.test(s || "") && Number.isSafeInteger(Number(s)) && Number(s) > 0
    ? Number(s)
    : 0;
const validDate = (s) =>
  /^\d{4}-\d{2}-\d{2}$/.test(s || "") &&
  !isNaN(Date.parse(s)) &&
  new Date(s).toISOString().slice(0, 10) === s;
// Ссылка с датой открывает именно этот день: делятся обычно одним днём,
// а не неделей, и на телефоне список дня — вид по умолчанию.
const sharedDate = validDate(query.get("date")) ? query.get("date") : "";
const state = {
  ...pref,
  group: validID(query.get("group")) || pref.group || betaProfile?.group_id || (config.demo ? 39 : 0),
  compare: validID(query.get("compare")) || pref.compare,
  subgroup: query.has("group")
    ? validID(query.get("subgroup")) || 0
    : (pref.group ? pref.subgroup : betaProfile?.subgroup_id || pref.subgroup),
  compareSubgroup: query.has("compare")
    ? validID(query.get("compare_subgroup")) || 0
    : pref.compareSubgroup,
  week: monday(
    validDate(query.get("week")) ? query.get("week") : sharedDate || today(),
  ),
  date: sharedDate || (validDate(query.get("week")) && monday(query.get("week")) !== monday(today()) ? query.get("week") : today()),
  route: "schedule",
  teacher: validID(query.get("teacher")) || 0,
  // Предмет, открытый поверх расписания. Из карточки пары приходит только
  // название: идентификатора справочника в занятии нет. Ссылкой делятся уже с
  // найденным id, поэтому в адресе живёт он.
  subject: validID(query.get("subject")) || 0,
  subjectName: query.get("subject_name") || "",
  subjectDate: validDate(query.get("subject_date")) ? query.get("subject_date") : "",
  subjectFilter: "auto",
  subjectLimit: 12,
  subjectData: null,
  upcoming: null,
  upcomingKey: "",
  upcomingError: false,
  groupInfo: null,
  otherInfo: null,
  subgroups: [],
  otherSubgroups: [],
  data: null,
  other: null,
  teacherData: null,
  teacherResults: [],
  teacherCatalog: null,
  teacherProfileOpen: null,
  teacherSubject: query.get("teacher_subject") || "",
  teacherScope: query.get("teacher_scope") === "mine" ? "mine" : "all",
  minMeeting: [15, 30, 60, 90].includes(Number(query.get("min"))) ? Number(query.get("min")) : pref.minMeeting,
  meetingEnd: [1080, 1200, 1320].includes(Number(query.get("until"))) ? Number(query.get("until")) : pref.meetingEnd,
  compareMode: ["nearby", "breaks", "free"].includes(query.get("mode"))
    ? query.get("mode")
    : pref.compareMode,
  proximity: ["all", "floor", "room"].includes(query.get("near"))
    ? query.get("near")
    : pref.proximity,
  loading: false,
  error: "",
  teacherQuery: query.get("teacher_q") || "",
  // Дни показанной недели, которые вуз недавно правил. Приходят с сервера,
  // поэтому работают и при первом заходе, и с нового устройства.
  revisionMode: false,
  revisionOnlyChanges: false,
  revisionDetailsOpen: false,
  revisions: [],
  revisionID: 0,
  revisionFollowLatest: true,
  revision: null,
  revisionContext: "",
  revisionLoading: false,
  revisionError: "",
  // Сессия: экзамены, зачёты и консультации на полгода вперёд. Пусто вне
  // сессии — и тогда экран о ней молчит целиком.
  exams: [],
};
if (sharedDate) state.view = "day";
if (config.demo && !state.compare) state.compare = 40;
try {
  if (
    !prefStorage.getItem("mp.preferences.v1") &&
    matchMedia("(max-width: 650px)").matches
  )
    state.view = "day";
} catch {}
if (betaProfile?.group_id && !state.homeGroup) state.homeGroup = {id: betaProfile.group_id, name: ""};
const data = createData({ demo: config.demo, storage: config.beta ? undefined : storage,
 fetcher: async (...args) => { const r = await fetch(...args); if(config.beta && r.status === 401) { document.body.replaceChildren(); location.replace("/login"); } return r; } });
let generation = 0,
  searchGeneration = 0,
  pickerGeneration = 0,
  pickerTarget = "group",
  pickerItems = [],
  pickerTimer,
  searchTimer,
  toastTimer,
  telegramTheme;
function persist() {
  if (state.group) {
    state.groupSubgroups[state.group] = state.subgroup;
    const pinned = new Set([state.group, state.homeGroup?.id, ...state.favorites.map(g => g.id)]);
    const entries = Object.entries(state.groupSubgroups);
    const keep = entries.filter(([id]) => pinned.has(Number(id)));
    state.groupSubgroups = Object.fromEntries([
      ...entries.filter(([id]) => !pinned.has(Number(id))).slice(-Math.max(0, 24 - keep.length)), ...keep,
    ]);
  }
  try {
    prefStorage.setItem(
      "mp.preferences.v1",
      JSON.stringify(
        Object.fromEntries(
          [
            "group",
            "subgroup",
            "compare",
            "compareSubgroup",
            "compareMode",
            "proximity",
            "theme",
            "view",
            "showTransfers",
            "favorites",
            "groupSubgroups",
            "homeGroup",
            "minMeeting",
            "meetingEnd",
          ].map((k) => [k, state[k]]),
        ),
      ),
    );
    if (!config.demo)
      document.cookie = `mp_group=${state.group}; Max-Age=31536000; Path=/; SameSite=Lax${location.protocol === "https:" ? "; Secure" : ""}`;
  } catch {
    toast("Браузер не разрешил сохранить настройки");
  }
}
const themes = [
  { value: "system", name: "Системная", icon: "system" },
  { value: "light", name: "Светлая", icon: "sun" },
  { value: "dark", name: "Тёмная", icon: "moon" },
  { value: "night", name: "Звёздная ночь", icon: "star" },
];
function nextTheme() {
  return themes.find(t => t.value === nextThemeValue(state.theme, document.documentElement.dataset.theme));
}
function syncThemeControls() {
  const toggle = $("#theme-toggle");
  if (toggle) {
    const current = themes.find((t) => t.value === state.theme);
    toggle.innerHTML = icon(current.icon);
    toggle.title = `Тема: ${current.name}. Включить: ${nextTheme().name}`;
    toggle.ariaLabel = toggle.title;
  }
  const select = $("#theme-select");
  if (select) select.value = state.theme;
}
function applyTheme() {
  const theme =
    state.theme === "system"
      ? telegramTheme ||
        (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
      : state.theme;
  document.documentElement.dataset.theme = theme;
  $('meta[name="theme-color"]').content =
    theme === "night" ? "#121525" : theme === "dark" ? "#202620" : "#f6f7f4";
  const manifest = $('link[rel="manifest"]');
  if (manifest) manifest.href = theme === "light" ? "/manifest.webmanifest" : `/manifest-${theme}.webmanifest`;
  syncThemeControls();
}
applyTheme();
matchMedia("(prefers-color-scheme: dark)").addEventListener(
  "change",
  applyTheme,
);
function toast(text) {
  if (!text) return;
  $("#toast").textContent = text;
  $("#toast").classList.add("visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => $("#toast").classList.remove("visible"), 3500);
}
// Текущая минута для отметки «сейчас». Intl-форматтер дороже, чем кажется,
// а за одну отрисовку её спрашивают десятки карточек.
let minuteCache = { at: 0, minute: 0 };
function nowMinute() {
  const at = Date.now();
  if (at - minuteCache.at > 1000) minuteCache = { at, minute: minutesNow() };
  return minuteCache.minute;
}
function routeFromURL() {
  const route = location.hash.slice(1);
  return nav.some((n) => n[0] === route) ? route : "schedule";
}
function urlFor(route = state.route) {
  const url = new URL(location.origin + location.pathname);
  if (state.group) url.searchParams.set("group", state.group);
  if (state.subgroup) url.searchParams.set("subgroup", state.subgroup);
  url.searchParams.set("week", state.week);
  if (route === "schedule" && state.view === "day")
    url.searchParams.set("date", state.date);
  if (route === "compare" && state.compare) {
    url.searchParams.set("compare", state.compare);
    url.searchParams.set("mode", state.compareMode);
    url.searchParams.set("near", state.proximity);
    url.searchParams.set("min", state.minMeeting);
    url.searchParams.set("until", state.meetingEnd);
    if (state.compareSubgroup)
      url.searchParams.set("compare_subgroup", state.compareSubgroup);
  }
  if (route === "teachers") {
    if (state.teacher) url.searchParams.set("teacher", state.teacher);
    if (state.teacherSubject && state.teacher) url.searchParams.set("teacher_subject", state.teacherSubject);
    if (state.teacherQuery) url.searchParams.set("teacher_q", state.teacherQuery);
    if (state.teacherScope === "mine") url.searchParams.set("teacher_scope", "mine");
  }
  if (route === "schedule" && (state.subject || state.subjectName)) {
    if (state.subject) url.searchParams.set("subject", state.subject);
    else url.searchParams.set("subject_name", state.subjectName);
    url.searchParams.set("subject_date", state.subjectDate || state.date);
  }
  url.hash = route;
  return url;
}
function syncURL() {
  history.replaceState(null, "", urlFor());
}
async function navigate(route) {
  if (route === "schedule") {
    state.subject = 0;
    state.subjectName = "";
    state.subjectData = null;
  }
  state.route = route;
  history.pushState(null, "", urlFor(route));
  window.scrollTo({ top: 0 });
  await load();
  focusHeading();
}
window.addEventListener("popstate", () => {
  state.route = routeFromURL();
  const q = new URLSearchParams(location.search);
  const back = validDate(q.get("date")) ? q.get("date") : "";
  state.week = monday(
    validDate(q.get("week")) ? q.get("week") : back || today(),
  );
  state.date = back || state.week;
  state.view = back ? "day" : "week";
  state.group = validID(q.get("group")) || state.group;
  state.subgroup = validID(q.get("subgroup")) || 0;
  state.compareMode = ["nearby", "breaks", "free"].includes(q.get("mode"))
    ? q.get("mode")
    : "nearby";
  state.proximity = ["all", "floor", "room"].includes(q.get("near"))
    ? q.get("near")
    : "all";
  state.compare = validID(q.get("compare")) || state.compare;
  state.compareSubgroup = validID(q.get("compare_subgroup")) || 0;
  state.minMeeting = [15, 30, 60, 90].includes(Number(q.get("min"))) ? Number(q.get("min")) : 30;
  state.meetingEnd = [1080, 1200, 1320].includes(Number(q.get("until"))) ? Number(q.get("until")) : 1080;
  state.teacher = validID(q.get("teacher")) || 0;
  state.teacherSubject = q.get("teacher_subject") || "";
  state.teacherQuery = q.get("teacher_q") || "";
  state.teacherScope = q.get("teacher_scope") === "mine" ? "mine" : "all";
  state.subject = validID(q.get("subject")) || 0;
  state.subjectName = q.get("subject_name") || "";
  state.subjectDate = validDate(q.get("subject_date")) ? q.get("subject_date") : "";
  state.subjectFilter = "auto";
  state.subjectLimit = 12;
  return load();
});

const button = (action, label, ico = "", cls = "", extra = "") =>
  `<button class="button ${cls}" data-action="${action}" ${extra}>${ico ? icon(ico) : ""}${label}</button>`;
const ib = (action, name, label, extra = "") =>
  `<button class="icon-button" data-action="${action}" aria-label="${esc(label)}" title="${esc(label)}" ${extra}>${icon(name)}</button>`;
function subgroupLabel() {
  return state.subgroups.find(s => s.id === state.subgroup)?.name || "Все подгруппы";
}
function groupSwitch(target = "group") {
  const g = target === "group" ? state.groupInfo : state.otherInfo;
  return `<button class="group-switch" data-action="pick" data-target="${target}"><span class="group-icon">${icon("users")}</span><span><small>${target === "group" ? (state.homeGroup?.id === state.group ? "Моя группа" : "Группа") : "Группа друга"}</small><strong>${esc(g?.name || "Выбрать группу")}</strong>${target === "group" && state.subgroups.length ? `<span class="mobile-subgroup">${esc(subgroupLabel())}</span>` : ""}</span>${icon("down")}</button>`;
}
// Группа, подгруппа и звёздочка отвечают на один вопрос — «чьё это
// расписание». Поэтому они стоят вместе в шапке, а не отдельной панелью над
// сеткой: та панель занимала на телефоне целую строку ради одного селекта.
function groupControls() {
  if (!state.group) return groupSwitch();
  const saved = state.favorites.some((g) => g.id === state.group);
  return `<div class="group-controls">${groupSwitch()}${state.subgroups.length ? `<label class="subgroup-field"><small>Подгруппа</small><select id="subgroup" aria-label="Моя подгруппа">${options(state.subgroups, state.subgroup)}</select></label>` : ""}${ib("favorite", "star", saved ? "Убрать группу из избранного" : "Сохранить группу в избранное", `aria-pressed="${saved}"`)}${state.homeGroup && state.homeGroup.id !== state.group ? button("home-group", "К моей группе", "left", "home-group") : ""}</div>`;
}
function options(subs, current) {
  return `<option value="0">Все подгруппы</option>${subs.map((s) => `<option value="${s.id}" ${s.id === current ? "selected" : ""}>${esc(s.name)}</option>`).join("")}`;
}
function notice(d) {
  if (!d) return "";
  if (d.offline)
    return `<div class="notice warning">${icon("wifi")}<span>Сохранённая копия от ${esc(dateLabel(d.cached_at.slice(0, 10)))}. Свежие данные недоступны; расписание могло измениться.</span>${button("reload", "Обновить")}</div>`;
  if (d.missing)
    return `<div class="notice warning">${icon("info")}<span>Часть расписания ещё не загружена. Пустые дни могут оказаться учебными.</span>${button("reload", "Повторить")}</div>`;
  if (d.stale)
    return `<div class="notice warning">${icon("clock")}<span>Данные давно не обновлялись. Возможны изменения в расписании.</span>${button("reload", "Проверить")}</div>`;
  return "";
}
function heading(title, subtitle = "", right = "") {
  return `<div class="page-heading"><div><h1>${title}</h1>${subtitle ? `<p>${subtitle}</p>` : ""}</div>${right}</div>`;
}
// Диапазон недели без повтора месяца: «7 – 13 сент.» вместо «7 сент. — 13
// сент.». Четыре недели из пяти лежат внутри одного месяца, а на телефоне
// строка со стрелками и переключателем вида съезжает именно из-за этих лишних
// символов.
function weekRange() {
  const from = state.week,
    to = shift(state.week, 6);
  return from.slice(0, 7) === to.slice(0, 7)
    ? `${dateLabel(from, { day: "numeric" })} – ${dateLabel(to, { day: "numeric", month: "short" })}`
    : `${dateLabel(from, { day: "numeric", month: "short" })} – ${dateLabel(to, { day: "numeric", month: "short" })}`;
}
// Сегодня доступно и в воскресенье, даже если API не включает выходной.
// В остальных неделях сохраняем ближайший доступный день.
function nearestDay(date, days) {
  const known = (days || []).filter((d) => d.date);
  if (date === today() || !known.length || known.some((d) => d.date === date)) return date;
  return (known.filter((d) => d.date < date).pop() || known[0]).date;
}
// Открыто ли ровно то, к чему ведёт «Сегодня».
function atToday() {
  if (state.week !== monday(today())) return false;
  if (state.route !== "schedule" || state.view !== "day") return true;
  return state.date === nearestDay(today(), state.data?.week?.days);
}
function weekControls(extra = "") {
  // «Сегодня» показываем, только когда есть куда возвращаться. На текущей
  // неделе кнопка не делает ничего, а место в строке занимает — и именно из-за
  // неё переключатель «Неделя · День» уезжал на телефоне за край.
  return `<div class="toolbar"><div class="date-controls">${ib("prev", "left", "Предыдущая неделя")}<span class="date-title">${weekRange()}</span>${ib("next", "right", "Следующая неделя")}${atToday() ? "" : button("today", "<span>Сегодня</span>", "refresh", "today-button", 'aria-label="Вернуться к текущей неделе"')}</div>${extra}</div>`;
}
function empty(title, text, action = "") {
  return `<section class="empty-state">${icon("calendar")}<h2>${title}</h2><p>${text}</p>${action}</section>`;
}
function welcome() {
  return `<section class="welcome"><span class="eyebrow muted">Ваш университет. Ваш ритм.</span><h1 style="margin-top:18px">Пары — по плану.<br>Время — для себя.</h1><p class="lead">Выберите группу один раз. Расписание, преподаватели и время для встреч будут под рукой при каждом возвращении.</p>${button("pick", "Найти мою группу", "search", "primary", 'data-target="group"')}<div class="welcome-features"><article>${icon("calendar")}<h3>Неделя целиком</h3><p>Все пары, окна и аудитории на одном экране.</p></article><article>${icon("compare")}<h3>Время для встречи</h3><p>Общие свободные часы с другой группой.</p></article><article>${icon("chart")}<h3>Понятная нагрузка</h3><p>Учебные часы и самые насыщенные дни.</p></article></div><p class="small muted" style="margin-top:24px">Настройки сохраняются только в этом браузере. Без регистрации.</p></section>`;
}
function universityMark() {
  const name = (config.university || "Вуз").trim();
  return name.length <= 3 ? esc(name) : teacherInitials(name);
}
function botsSection() {
  return `<div class="bot-links">${botLinks.map((link) => `<a href="${esc(botLink(link, state.group))}" target="_blank" rel="noopener noreferrer" aria-label="${link.label}"><span class="bot-mark" aria-hidden="true">${link.mark}</span>${link.name}${icon("up")}</a>`).join("")}</div>`;
}
let lastHTML = "";
let pendingFocus = null;
let detailFocus = null;
function focusIdentity(el) {
  if (!el || el === document.body || !el.matches?.("button, a, input, select, summary, h1")) return null;
  return { id: el.id, tag: el.tagName?.toLowerCase(), data: { ...el.dataset }, label: el.getAttribute?.("aria-label") };
}
function restoreFocus(identity) {
  if (!identity || !document.querySelectorAll) return false;
  const candidates = document.querySelectorAll("button, a, input, select, summary, h1");
  const match = [...candidates].find(el => identity.id ? el.id === identity.id :
    el.tagName.toLowerCase() === identity.tag &&
    (Object.keys(identity.data).length ? Object.entries(identity.data).every(([k, v]) => el.dataset[k] === v) :
      identity.label && el.getAttribute("aria-label") === identity.label));
  if (!match) return false;
  match.focus({ preventScroll: true });
  return true;
}
function focusHeading() {
  const heading = $("#main h1");
  heading?.setAttribute?.("tabindex", "-1");
  heading?.focus?.({ preventScroll: true });
}
const scheduleScrollLeft = new Map();
function render() {
  const active = state.route,
    links = nav
      .map(
        ([key, ico, title]) =>
          `<a href="${esc(urlFor(key))}" data-route="${key}" class="nav-link ${active === key ? "active" : ""}" ${active === key ? 'aria-current="page"' : ""}>${icon(ico)}${title}</a>`,
      )
      .join("");
  const html = `<div class="app-shell route-${active}"><aside class="sidebar"><a class="brand" href="#schedule" data-route="schedule"><span class="brand-symbol">${esc(config.web_mark || (Array.from((config.app_name || "Между парами").trim())[0] || "м").toLowerCase() + ".")}</span><span class="brand-name">${esc(config.app_name || "Между парами")}<small>РАСПИСАНИЕ ${esc(config.university || "Университет")}</small></span></a><nav aria-label="Основная навигация">${links}${adminAccess ? `<a class="nav-link" href="/admin/">${icon("settings")}Админ-панель</a>` : ""}</nav><section><p class="side-label">ИЗБРАННЫЕ ГРУППЫ</p>${state.favorites.length ? state.favorites.map((g) => `<button class="favorite" data-action="favorite-open" data-id="${g.id}">${esc(g.name)}</button>`).join("") : '<p class="small" style="padding:0 14px;color:var(--sidebar-muted);line-height:1.6">Сохраните свою группу<br>или группу друга ☆</p>'}</section><div class="side-bottom">${botLinks.length ? `<div class="side-bots"><p class="side-label">РАСПИСАНИЕ В БОТЕ</p>${botsSection()}</div>` : ""}<a href="/source" class="small">Исходный код и лицензия</a><div class="side-footer"><span class="dot"></span>${esc(config.location_label || "Мой университет")}</div></div></aside><div class="page"><header class="topbar"><div class="breadcrumb">${esc(config.university || "Университет")} ${icon("right")} <span>${nav.find((n) => n[0] === active)?.[2]}</span></div><div class="mobile-brand"><span class="brand-symbol">${esc(config.web_mark || (Array.from((config.app_name || "Между парами").trim())[0] || "м").toLowerCase() + ".")}</span>${esc(config.app_name || "Между парами")}</div><div class="top-actions">${ib("theme", "sun", "Тема оформления", 'id="theme-toggle"')}${button("share", "<span>Поделиться</span>", "share", "", 'aria-label="Поделиться расписанием"')}<span class="avatar" aria-hidden="true">${universityMark()}</span></div></header><main id="main">${config.demo ? `<div class="notice demo">${icon("info")}<span>Демонстрация · Вымышленное расписание для знакомства с сайтом.</span></div>` : ""}${state.loading ? '<div class="loading-line" role="status" aria-label="Загрузка"></div>' : ""}${content()}</main></div><nav class="bottom-nav" aria-label="Мобильная навигация">${nav.map(([key, ico, , title]) => `<a href="${esc(urlFor(key))}" data-route="${key}" class="${active === key ? "active" : ""}" ${active === key ? 'aria-current="page"' : ""}>${icon(ico)}${title}</a>`).join("")}</nav></div>`;
  // Фоновое обновление приходит каждые пять минут и при возврате на вкладку.
  // Пока разметка та же, DOM не трогаем вовсе: замена innerHTML сбрасывает
  // прокрутку и фокус, и именно она читалась как «сайт мигает».
  if (html !== lastHTML) {
    pendingFocus = focusIdentity(document.activeElement) || pendingFocus;
    const top = window.scrollY || 0;
    if (active !== "schedule") scheduleScrollLeft.clear();
    const horizontal = [".schedule-scroll", ".day-tabs"].map(selector => {
      const scroller = $(selector);
      if (scroller) scheduleScrollLeft.set(selector, scroller.scrollLeft || 0);
      return [selector, scheduleScrollLeft.get(selector) || 0];
    });
    const app = $("#app");
    // A loading state or an empty day can be shorter than the viewport's
    // current position. Reserve just enough height BEFORE replacing the DOM,
    // so the browser cannot clamp scrollY and lose it between async renders.
    // Recompute on each render: scrolling up or opening another page releases
    // the space, and scrolling during a request remains under user control.
    app.style.minHeight = top > 0 ? `${top + window.innerHeight}px` : "";
    app.innerHTML = html;
    lastHTML = html;
    if (restoreFocus(pendingFocus) || !state.loading) pendingFocus = null;
    window.scrollTo({ top, left: window.scrollX || 0, behavior: "instant" });
    for (const [selector, left] of horizontal) {
      const scroller = $(selector);
      if (scroller) scroller.scrollLeft = left;
    }
  }
  syncThemeControls();
  document.title = `${active === "schedule" && (state.subject || state.subjectName) ? state.subjectData?.discipline?.name || state.subjectName || "Про предмет" : nav.find((n) => n[0] === active)?.[2]}${state.groupInfo ? " · " + state.groupInfo.name : ""} — ${config.app_name || "Между парами"}`;
  backButton(active !== "schedule" || !!(state.subject || state.subjectName));
}
function content() {
  if (state.route === "settings") return settingsView();
  if (state.error)
    return (
      heading("Не получилось загрузить") +
      empty(
        "Давайте попробуем ещё раз",
        esc(state.error),
        button("reload", "Повторить", "refresh", "primary") +
          " " +
          button("pick", "Другая группа", "", "", 'data-target="group"'),
      )
    );
  if (state.route === "teachers") return teachersView();
  if (!state.group) return welcome();
  // Предмет живёт поверх расписания и не ждёт недели: он про полугодие.
  if (state.route === "schedule" && (state.subject || state.subjectName))
    return subjectView();
  if (!state.data)
    return (
      heading("Ваше расписание", "Загружаем неделю…", groupSwitch()) +
      '<p class="progress-text" role="status">Расписание скоро появится</p>'
    );
  if (state.route === "compare") return compareView();
  if (state.route === "stats") return statsView();
  return scheduleView();
}
let lastLoadAt = 0;
// quiet — фоновое обновление: страницу не опустошаем и об ошибке не кричим.
// Человек продолжает читать то, что уже открыто, а новое приезжает на его
// место одним движением, если действительно отличается.
async function load({ quiet = false, soft = false } = {}) {
  const g = ++generation;
  ++searchGeneration;
  clearTimeout(searchTimer);
  state.error = "";
  if (!quiet) {
    state.loading = true;
    // soft — переход внутри уже открытого расписания: соседняя неделя, другой
    // день. Страницу не опустошаем: пустой экран на полсекунды обнуляет высоту
    // документа, браузер прижимает прокрутку к началу, и переход читается как
    // перезагрузка сайта. Подгруппу так не переключаем — показать чужие пары
    // даже на один кадр хуже, чем показать пустое место.
    if (!soft) {
      state.data = null;
      state.other = null;
      state.teacherData = null;
      state.exams = [];
      state.subjectData = null;
      // Вложенную неделю берём ровно один раз и только если она про то же
      // самое, что человек сейчас открывает: подгруппу сервер знает лишь из
      // ссылки, а не из памяти браузера.
      if (
        !preloadUsed &&
        state.route === "schedule" &&
        preload?.week &&
        preload.group?.id === state.group &&
        (preload.subgroup?.id || 0) === state.subgroup &&
        preload.week.monday === state.week
      ) {
        preloadUsed = true;
        state.data = preload;
        state.groupInfo = preload.group;
      }
    }
    render();
  }
  const group = state.group,
    other = state.compare,
    teacher = state.teacher,
    week = state.week,
    route = state.route,
    subgroup = state.subgroup,
    otherSubgroup = state.compareSubgroup,
    subject = state.subject,
    subjectName = state.subjectName,
    // Полугодие считается от показанного дня, а не от сегодняшнего: открыв
    // предмет из майской недели, человек спрашивает про весенний семестр.
    anchor = state.subjectDate || state.date;
  try {
    let result = {};
    if (route === "settings" && group) {
      const [info, roster] = await Promise.all([data.request("/groups/get", { group }), data.request("/groups/subgroups", { group })]);
      result.groupInfo = info;
      result.subgroups = roster.subgroups || [];
    } else if (route === "teachers") {
      if (teacher)
        result.teacherData = await data.request("/teachers/week", {
          teacher,
          monday: week,
        });
      else {
        result.teacherCatalog = await data.request("/teachers/search", teacherSearchParams());
        result.teacherResults = result.teacherCatalog.teachers || [];
      }
    } else if (group && route === "schedule" && (subject || subjectName)) {
      const [discipline, roster] = await Promise.all([
        data.request("/disciplines/get", {
          group, subgroup, date: anchor,
          ...(subject ? { discipline: subject } : { q: subjectName }),
        }).catch(err => ({ failed: true, notFound: err.status === 404 })),
        data.request("/groups/subgroups", { group }),
      ]);
      result.subjectData = discipline;
      result.groupInfo = discipline.group || state.groupInfo;
      result.subgroups = roster.subgroups || [];
    } else if (group && route !== "settings") {
      const responses = await Promise.all([
        data.request("/schedule/week", { group, subgroup, monday: week }),
        data.request("/groups/subgroups", { group }),
        route === "schedule"
          ? data
              .request("/schedule/exams", { group, subgroup })
              .catch(() => ({}))
          : Promise.resolve({}),
      ]);
      result.data = responses[0];
      result.groupInfo = responses[0].group;
      result.subgroups = responses[1].subgroups || [];
      // Порядок сессии — обещание «ближайшее сверху». Сервер его держит, но
      // ошибиться здесь значит назвать не тот экзамен, поэтому сортируем сами.
      result.exams = [...(responses[2].items || [])].sort(
        (a, b) =>
          a.date.localeCompare(b.date) ||
          a.minute_from - b.minute_from ||
          a.id - b.id,
      );
      if (route === "compare" && other) {
        const rs = await Promise.all([
          data.request("/schedule/week", {
            group: other,
            subgroup: otherSubgroup,
            monday: week,
          }),
          data.request("/groups/subgroups", { group: other }),
        ]);
        result.other = rs[0];
        result.otherInfo = rs[0].group;
        result.otherSubgroups = rs[1].subgroups || [];
      }
    }
    if (g !== generation) return;
    Object.assign(state, result);
    // IDs of subgroups can change when a group moves to a new catalog entry.
    if (
      (state.data || state.subjectData) &&
      state.subgroup &&
      !state.subgroups.some((s) => s.id === state.subgroup)
    ) {
      state.subgroup = 0;
      persist();
      toast("Подгруппа больше не найдена. Показаны все подгруппы.");
      return load();
    }
    if (
      state.other &&
      state.compareSubgroup &&
      !state.otherSubgroups.some((s) => s.id === state.compareSubgroup)
    ) {
      state.compareSubgroup = 0;
      persist();
      return load();
    }
    if (state.date < week || state.date > shift(week, 6)) state.date = week === monday(today()) ? today() : week;
    // Держать выбранным день, которого нет в неделе (то же воскресенье), —
    // значит показать пустой список и активную вкладку, которой не видно.
    state.date = nearestDay(state.date, state.data?.week?.days);
    if (state.homeGroup?.id === state.group && state.groupInfo) state.homeGroup.name = state.groupInfo.name;
    if (state.data) state.data = withAudienceNames(state.data, state.subgroups);
    // Подпись подгруппы разрешается по справочнику здесь же: лента предмета
    // показывает те же пары, что и сетка, и называть их иначе нельзя.
    if (state.subjectData?.items)
      state.subjectData = {
        ...state.subjectData,
        items: state.subjectData.items.map((l) => ({
          ...l,
          audience_label: audienceName(
            l,
            state.subgroups,
            state.groupInfo?.name,
          ),
        })),
      };
    // Ссылка на предмет должна открываться и без названия под рукой.
    if (state.subjectData?.discipline?.id)
      state.subject = state.subjectData.discipline.id;
    if (state.other)
      state.other = withAudienceNames(state.other, state.otherSubgroups);
  } catch (err) {
    // Сорвавшееся фоновое обновление не повод убрать с экрана расписание,
    // которое человек уже читает.
    if (g === generation && !quiet)
      state.error =
        err.name === "AbortError"
          ? "Сервер отвечает дольше обычного. Сохранённой копии этой недели пока нет."
          : err.message;
  }
  if (g !== generation) return;
  if (state.revisionMode && state.route === "schedule") await loadRevisions();
  if (g !== generation) return;
  state.loading = false;
  lastLoadAt = Date.now();
  syncURL();
  render();
  // Paint the week first; a failed extra summary must never block the schedule.
  if (route === "schedule" && group && !subject && !subjectName && week === monday(today()) && !state.error) {
    const key = `${group}:${subgroup}:${today()}`;
    if (state.upcomingKey !== key) { state.upcoming = null; state.upcomingError = false; }
    state.upcomingKey = key;
    try {
      const upcoming = await data.request("/schedule/upcoming", { group, subgroup, date: today() });
      if (g !== generation) return;
      state.upcoming = upcoming;
      state.upcomingError = false;
    } catch {
      if (g !== generation) return;
      state.upcomingError = true;
      if (state.upcoming) state.upcoming = { ...state.upcoming, stale: true };
    }
    render();
  }
}
// Единая точка для фоновых обновлений: не дёргаем сервер чаще раза в минуту,
// не перебиваем ввод и не выдёргиваем страницу из-под открытого диалога.
function refresh(minAge = 60000) {
  if (
    document.visibilityState !== "visible" ||
    Date.now() - lastLoadAt < minAge ||
    document.activeElement?.matches("input, select, textarea") ||
    $("#picker").open ||
    $("#detail").open
  )
    return;
  return load({ quiet: true });
}
function lessonCard(l, day = false) {
  l = {
    ...l,
    audience_label: audienceName(
      l,
      state.route === "teachers" ? [] : state.subgroups,
      state.route === "teachers" ? (l.groups || []).map(g => g.name).join(", ") : state.groupInfo?.name,
    ),
  };
  const kind = lessonKind(l.class_type),
    cls = kind.key,
    label = kind.label;
  const running =
    !l.revisionKind && actual(l) &&
    l.date === today() &&
    l.minute_to > l.minute_from &&
    l.minute_from <= nowMinute() &&
    nowMinute() < l.minute_to;
  if (!actual(l))
    return `<div class="free-day">${icon("coffee")}${l.flags & 8 ? "Неучебное время" : "Окно"}<span>${clock(l.minute_from)} – ${clock(l.minute_to)}</span></div>`;
  return `<button class="lesson ${cls} ${l.revisionKind ? "revision-lesson revision-" + l.revisionKind : ""} ${running ? "now" : ""} ${day && !l.revisionKind ? "with-time-rail" : ""}" data-action="${l.revisionKind ? "revision-lesson" : "lesson"}" data-id="${l.id}" data-kind="${l.revisionKind || ""}"><span class="time">${l.minute_to > l.minute_from ? `${clock(l.minute_from)} – ${clock(l.minute_to)}` : "Время не указано"}<span>${l.number || ""}</span></span>${l.revisionKind ? `<span class="revision-card-mark">${l.revisionKind === "added" ? "+ Стало" : l.revisionKind === "removed" ? "− Было" : "= Без изменений"}</span>` : ""}<strong class="lesson-title">${esc(l.discipline || "Занятие")}</strong><span class="lesson-meta">${esc(placeLabel(l))}</span><span class="lesson-meta">${esc((l.staff || []).length > 2 ? shortName(l.staff[0]) + " и ещё " + (l.staff.length - 1) : (l.staff || []).map(shortName).join(", ") || "Преподаватель не указан")}</span><span class="lesson-type">${esc(label)}${l.subgroup_id ? " · " + esc(l.audience_label || "Подгруппа") : ""}${l.flags & 2 ? " · Самоподготовка" : ""}</span></button>`;
}
// Тонкая полоса вместо пары: окно между занятиями или пустые слоты до первой
// пары. Второе важнее первого: без него расписание с началом в 10:25 выглядит
// как расписание с началом в 8:30.
function freeSlot(free) {
  const n = free.slots.length,
    word = n === 1 ? "пара" : n < 5 ? "пары" : "пар",
    label = free.lead ? "свободно" : "окно",
    title = free.lead
      ? `С ${clock(free.from)} пар нет, первая начинается позже`
      : `Окно ${clock(free.from)} – ${clock(free.to)}`;
  return `<div class="slot-free${free.lead ? " lead" : ""}" title="${title}"><b>${clock(free.from)} – ${clock(free.to)}</b><span>${label} · ${n} ${word}</span></div>`;
}
// Подсказка о смене корпуса нужна не всем: соседние корпуса могут стоять
// впритык. Показываем её по желанию; указываем длину перемены, а не время в пути.
function transferNote(prev, next) {
  if (!state.showTransfers || !prev || !next || prev.number === next.number) return "";
  const from = parseClassroom(prev.classroom),
    to = parseClassroom(next.classroom);
  if (!from || !to || from.building === to.building) return "";
  const gap = next.minute_from - prev.minute_to;
  if (gap <= 0 || gap > 60) return "";
  return `<div class="transfer" title="Перемена ${gap} мин между корпусами ${esc(from.building)} и ${esc(to.building)}">${icon("pin")}${gap} мин · другой корпус</div>`;
}
const nowIndex = (rows, date) =>
  date === today() ? nowRow(rows, nowMinute()) : -1;
const nowLine = () =>
  `<div class="now-line"><span>${clock(nowMinute())}</span></div>`;
function rowLessons(row, day = false) {
  const ls = row.lessons || [row.lesson];
  return `${ls.length > 1 ? `<p class="parallel-label">Одновременно · ${ls.length} занятия</p>` : ""}${ls.map(l => lessonCard(l, day)).join("")}`;
}
function dayColumn(day, grid) {
  const rows = groupedDayRows(day, grid);
  if (!rows.length) return `<div class="free-day">${icon("coffee")}Занятий нет в расписании</div>`;
  const mark = nowIndex(rows, day.date);
  return rows.map((row, i) => {
    return `<div class="schedule-row${row.lessons?.length > 1 ? " parallel" : ""}">${i === mark ? nowLine() : ""}${row.free ? freeSlot(row.free) :
      (row.gapBefore > 0 ? `<div class="gap">${icon("coffee")}${row.gapBefore} мин · окно</div>` : transferNote(rows[i - 1]?.lesson, row.lesson)) + rowLessons(row)}</div>`;
  }).join("");
}
function weekGrid(week, grid, edited = []) {
  return `<div class="schedule-scroll" tabindex="0" aria-label="Расписание недели; на узком экране прокручивается горизонтально"><div class="week-grid compact ${week.days?.length === 7 ? "seven" : ""}">${(week.days || []).map((d, i) => `<section class="day-column" data-date="${d.date}"><div class="day-heading ${d.date === today() ? "today" : ""}"><span>${dateLabel(d.date, { weekday: "short" })}${edited.includes(d.date) ? `<i class="edited-dot" title="Расписание этого дня недавно правили"></i>` : ""}</span><strong>${dateLabel(d.date, { day: "numeric" })}</strong></div><div class="day-contents">${dayColumn(d, grid)}</div></section>`).join("")}</div></div><div class="legend"><span><i></i>Лекции</span><span><i></i>Практика</span><span><i></i>Лабораторные</span>${(week.days || []).some((d) => (d.items || []).some((l) => sessionKind(l.class_type))) ? "<span><i></i>Сессия</span>" : ""}</div>`;
}
function dayList(week, grid) {
  const known = (week.days || []).map(d => d.date).filter(Boolean),
    dates = [...new Set([...(known.length ? known : Array.from({ length: 6 }, (_, i) => shift(state.week, i))), ...(state.week === monday(today()) ? [today()] : [])])].sort(),
    day = week.days?.find(d => d.date === state.date), rows = groupedDayRows(day, grid), mark = nowIndex(rows, state.date);
  return `<div class="day-tabs" aria-label="День недели">${dates.map(d => `<button data-action="day" data-date="${d}" class="${[state.date === d ? "active" : "", d === today() ? "today" : ""].filter(Boolean).join(" ")}" aria-pressed="${state.date === d}" ${d === today() ? 'aria-current="date"' : ""}>${dateLabel(d, { weekday: "short" })}<strong>${dateLabel(d, { day: "numeric" })}</strong>${d === today() ? '<span class="today-label">Сегодня</span>' : ""}</button>`).join("")}</div><div class="day-list">${rows.length ? rows.map((row, i) => `<div class="schedule-row">${i === mark ? nowLine() : ""}${!row.free ? transferNote(rows[i - 1]?.lesson, row.lesson) : ""}<div class="day-row${row.free ? " is-free" : ""}"><div class="day-time">${row.free ? clock(row.free.from) : row.lesson.minute_to > row.lesson.minute_from ? clock(row.lesson.minute_from) : "—"}<span>${row.free ? clock(row.free.to) : row.lesson.minute_to > row.lesson.minute_from ? clock(row.lesson.minute_to) : "—"}</span>${row.lesson?.number ? `<small>${row.lesson.number} пара</small>` : ""}</div><div class="${row.lessons?.length > 1 ? "parallel" : "day-cards"}">${row.free ? freeSlot(row.free) : rowLessons(row, true)}</div></div></div>`).join("") : empty("Занятий на этот день нет", "Если расписание ещё не опубликовано или не полностью загружено, пары могут появиться позже.")}</div>`;
}
function miniCalendar() {
  const first = state.week.slice(0, 8) + "01",
    start = monday(first);
  return `<section class="panel mini-calendar"><div class="month"><span>${dateLabel(first, { month: "long", year: "numeric" })}</span>${icon("calendar")}</div><div class="calendar-grid">${["пн", "вт", "ср", "чт", "пт", "сб", "вс"].map((d) => `<span>${d}</span>`).join("")}${Array.from(
    { length: 42 },
    (_, i) => shift(start, i),
  )
    .map(
      (d) =>
        `<button class="${d === today() ? "today" : monday(d) === state.week ? "selected" : ""}" data-action="calendar" data-date="${d}" aria-label="${dateLabel(d)}">${dateLabel(d, { day: "numeric" })}</button>`,
    )
    .join("")}</div></section>`;
}
function freshness(d = state.data) {
  return `<div class="freshness"><p>${icon("refresh")} ${config.demo ? "Пример данных" : d?.fetched_at && !d.fetched_at.startsWith("0001") ? "Обновлено " + dateLabel(d.fetched_at.slice(0, 10)) + ", " + new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", timeZone: config.timezone || "UTC" }).format(new Date(d.fetched_at)) : "Дата обновления пока неизвестна"}</p><p>Часовой пояс: ${esc(config.timezone || "UTC")}. Источник — ${esc(config.source_name || "расписание вуза")}. Возможны изменения.</p></div>`;
}
function myDayView() {
  const key = `${state.group}:${state.subgroup}:${today()}`;
  const saved = state.upcomingKey === key ? state.upcoming : null;
  const source = saved || { ...state.data, days: state.data.week.days };
  const day = studyDay(source.days, today(), nowMinute());
  const incomplete = day?.missing ?? source.missing;
  const old = source.offline || (day?.stale ?? source.stale) || (state.upcomingKey === key && state.upcomingError);
  const warning = incomplete ? "Расписание загружено частично: занятия могут быть и раньше." :
    old ? "По сохранённому расписанию. Возможны изменения." : "";
  if (!day) {
    const text = !saved ? (state.upcomingError ? "Не удалось проверить следующие дни. Откройте нужную неделю или повторите загрузку." : "Проверяем следующие 30 дней…") :
      incomplete ? "Часть расписания ещё не загружена. Пока нельзя определить следующий учебный день." :
      `До ${dateLabel(source.to, { day: "numeric", month: "long" })} занятий в ${old ? "сохранённом" : "загруженном"} расписании нет. Оно может дополниться позже.`;
    return `<section class="my-day" aria-label="Мой день"><div class="my-day-top"><span class="eyebrow">Мой день</span>${icon("calendar")}</div><h2>Следующий учебный день</h2><p class="my-day-note">${text}</p>${state.upcomingError ? button("reload", "Повторить", "refresh") : ""}</section>`;
  }
  const sum = dayOverview(day, source.grid || state.data.grid);
  const when = day.date === today() ? "Сегодня" : day.date === shift(today(), 1) ? "Завтра" : dateLabel(day.date, { weekday: "long" });
  const started = day.date === today() && sum.start !== null && nowMinute() >= sum.start;
  const places = [...new Set(sum.first.map(l => placeLabel(l)))];
  const focus = day.date === today() ? scheduleFocus({ monday: state.week, days: [day] }, today(), nowMinute()) : null;
  const current = focus?.lesson && state.data.week.days.some(d => d.items?.some(l => l.id === focus.lesson.id))
    ? `<button class="my-day-now" data-action="lesson" data-id="${focus.lesson.id}"><span>${focus.kind === "current" ? (old || incomplete ? "По расписанию" : "Сейчас") : "Дальше"}</span><strong>${esc(focus.lesson.discipline || "Занятие")}</strong><span>${focus.kind === "current" ? `ещё ${focus.remaining} мин` : focus.lesson.minute_to > focus.lesson.minute_from ? `в ${clock(focus.lesson.minute_from)}` : "время не указано"} · ${esc(placeLabel(focus.lesson))}</span>${icon("right")}</button>` : "";
  const todayLessons = (state.data.week.days.find(d => d.date === today())?.items || []).filter(actual);
  const finished = day.date > today() && todayLessons.length && todayLessons.every(l => l.minute_to > l.minute_from && l.minute_to <= nowMinute());
  const composition = `<ul class="my-day-kinds" aria-label="Состав дня">${sum.kinds.map(k => `<li><span>${esc(k.label)}</span><strong>${k.count}</strong></li>`).join("")}</ul>`;
  const windows = sum.windows.map(w => `${icon("coffee")}Окно ${clock(w.from)}–${clock(w.to)} · ${w.to-w.from} мин`);
  return `<section class="my-day" aria-label="Мой день"><div class="my-day-top"><span class="eyebrow">${day.date === today() ? "Мой день" : "Следующий учебный день"}</span><span>${dateLabel(day.date, { day: "numeric", month: "long" })}</span></div><div class="my-day-main"><div><h2>${when}${sum.start === null ? " · время уточняется" : ` ${started ? "с" : "к"} ${clock(sum.start)}`}</h2><p class="my-day-facts"><strong>${sum.count} ${plural(sum.count, "занятие", "занятия", "занятий")}</strong>${sum.end !== null ? `<span>до ${clock(sum.end)}</span>` : ""}${!state.subgroup && state.subgroups.length ? "<span>все подгруппы</span>" : ""}</p></div>${button("calendar", "Открыть день", "right", "my-day-open", `data-date="${day.date}"`)}</div>${places.length ? `<p class="my-day-place">${icon("pin")}${places.length > 1 ? "Первые занятия: " : "Первое занятие: "}${esc(places.join(" · "))}</p>` : ""}${composition}${windows.length ? `<details class="my-day-details"><summary>${sum.windows.length} ${plural(sum.windows.length, "окно", "окна", "окон")}</summary><ul>${windows.map(w => `<li>${w}</li>`).join("")}</ul></details>` : ""}${current}${finished ? '<p class="my-day-note">На сегодня пары закончились.</p>' : ""}${sum.unknown ? '<p class="my-day-note">У части занятий нет времени: начало, конец дня и окна пока неизвестны.</p>' : ""}${warning ? `<p class="my-day-note">${warning}</p>` : ""}</section>`;
}

function nextCard() {
  const focus = scheduleFocus(state.data.week, today(), nowMinute()),
    l = focus.lesson;
  const live =
    focus.kind === "current" &&
    !state.data.stale &&
    !state.data.missing &&
    !state.data.offline;
  const tag = l ? "button" : "article";
  const status = live
    ? `<span class="focus-status"><span class="dot"></span>Ещё ${focus.remaining} мин</span>`
    : icon(l ? "right" : "calendar");
  const emptyTitle = focus.kind === "past"
    ? "Архив расписания"
    : focus.kind === "done" && items(state.data.week).length
      ? "Все пары недели позади"
      : "Занятий в расписании нет";
  const caption = l
    ? `${dateLabel(l.date, { weekday: "short", day: "numeric", month: "short" })} · ${l.minute_to > l.minute_from ? clock(l.minute_from) : "Время не указано"} · ${esc(placeLabel(l))}`
    : focus.kind === "past"
      ? "Можно посмотреть расписание и нагрузку за эти даты."
      : focus.kind === "future"
        ? "Расписание на выбранную неделю может появиться позже."
        : "Предстоящих занятий в выбранной неделе нет.";
  return `<${tag} class="summary-card featured focus-card ${live ? "is-current" : ""}" ${l ? `data-action="lesson" data-id="${l.id}" aria-label="Подробнее: ${esc(l.discipline)}"` : ""}><div class="card-top"><span>${focus.title}</span>${status}</div><div class="subject">${esc(l?.discipline || emptyTitle)}</div><p class="caption">${caption}</p>${focus.kind === "next" && focus.wait !== null ? `<span class="focus-countdown">${focus.wait < 60 ? "Начнётся через " + focus.wait + " мин" : "Сегодня в " + clock(l.minute_from)}</span>` : ""}</${tag}>`;
}
const revisionStamp = (at) =>
  new Intl.DateTimeFormat("ru-RU", {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: config.timezone || "UTC",
  }).format(new Date(at));
let revisionGeneration = 0;
const revisionContext = () => `${state.group}:${state.subgroup}:${state.week}`;
async function loadRevisions(reset = false) {
  const key = revisionContext(),
    request = ++revisionGeneration;
  const focused = document.activeElement;
  const focusAction = focused?.dataset?.action;
  const focusID = focused?.id;
  if (reset || state.revisionContext !== key) {
    state.revisionID = 0;
    state.revisionFollowLatest = true;
    state.revision = null;
    state.revisions = [];
  }
  state.revisionContext = key;
  state.revisionLoading = true;
  state.revisionError = "";
  render();
  try {
    const list = await data.request("/changes/history", {
      group: state.group,
      monday: state.week,
    });
    if (request !== revisionGeneration || key !== revisionContext()) return;
    state.revisions = (list.revisions || []).filter(
      (r) => new Date(r.created_at).getTime() >= Date.now() - 14 * 86400000,
    );
    state.revisionOffline = !!list.offline;
    const selected =
      (state.revisionFollowLatest
        ? state.revisions[0]
        : state.revisions.find((r) => r.id === state.revisionID)) ||
      state.revisions[0];
    state.revisionID = selected?.id || 0;
    state.revision = null;
    if (selected) {
      const revision = await data.request("/changes/history", {
        group: state.group,
        subgroup: state.subgroup,
        monday: state.week,
        id: selected.id,
      });
      if (request !== revisionGeneration || key !== revisionContext()) return;
      state.revision = revision;
      state.revisionOffline ||= !!revision.offline;
    }
  } catch (err) {
    if (request !== revisionGeneration || key !== revisionContext()) return;
    state.revisionError =
      err.status === 404
        ? "Срок хранения снимка истёк. Обновите историю."
        : "Не удалось загрузить историю правок.";
  }
  if (request !== revisionGeneration || key !== revisionContext()) return;
  state.revisionLoading = false;
  render();
  if (focusID === "revision-select") $("#revision-select")?.focus?.({ preventScroll: true });
  else if (["revision-step", "revision-latest"].includes(focusAction))
    $("#revision-select")?.focus?.({ preventScroll: true });
  else if (focusAction === "revisions-toggle")
    $('[data-action="revisions-toggle"]')?.focus?.({ preventScroll: true });
}
function revisionsToggle() {
  return `<div class="revisions-toggle-row"><span>${icon("refresh")} История расписания <small>14 дней</small></span><button class="revisions-switch" role="switch" aria-checked="${state.revisionMode}" data-action="revisions-toggle"><span>Правки</span><i aria-hidden="true"></i></button></div>`;
}
function revisionsPanel() {
  if (state.revisionContext !== revisionContext() || state.revisionLoading)
    return '<section class="revisions-panel" role="status">Загружаем снимки расписания…</section>';
  if (state.revisionError)
    return `<section class="revisions-panel" role="status"><p>${state.revisionError}</p>${button("revisions-reload", "Повторить", "refresh")}</section>`;
  if (!state.revision)
    return `<section class="revisions-panel"><h2>Пока без снимков правок</h2><p>За последние 14 дней для этой недели нет сохранённых правок. Ниже — текущее расписание. История появится после следующего обнаруженного изменения.</p></section>`;
  const rev = state.revision,
    rows = revisionRows(rev, state.data?.grid),
    plus = rows.filter((l) => l.revisionKind === "added").length,
    minus = rows.filter((l) => l.revisionKind === "removed").length,
    unchanged = rows.filter((l) => l.revisionKind === "unchanged").length,
    index = state.revisions.findIndex((r) => r.id === rev.id);
  const summary = revisionSummary(rev);
  const summaryList = lines => `<ul class="revision-summary">${lines.map(text => `<li>${esc(text)}</li>`).join("")}</ul>`;
  const summaryHTML = summary.length ? summaryList(summary.slice(0, 5)) + (summary.length > 5 ? `<details class="revision-details"><summary>Ещё ${summary.length - 5} изменений</summary>${summaryList(summary.slice(5))}</details>` : "") : '<p class="small muted">В этой версии выбранная группа и подгруппа без изменений.</p>';
  return `<section class="revisions-panel" aria-label="История правок">
    <div class="revision-heading"><div><span class="eyebrow">${index === 0 ? "Последние изменения" : "Предыдущие изменения"}</span><h2>Что поменялось в расписании</h2></div></div>
    ${summaryHTML}
    <label class="revision-options"><input type="checkbox" id="revision-only" ${state.revisionOnlyChanges ? "checked" : ""}>Только изменённые занятия</label>
    <details id="revision-details" class="revision-details" ${state.revisionDetailsOpen ? "open" : ""}><summary>Версии расписания · ${esc(revisionStamp(rev.created_at))}</summary>
    <div class="revision-picker"><button data-action="revision-step" data-step="1" ${index === state.revisions.length - 1 ? "disabled" : ""} aria-label="Предыдущий снимок">${icon("left")}</button><select id="revision-select" aria-label="Снимок расписания">${state.revisions.map((r, i) => `<option value="${r.id}" ${r.id === rev.id ? "selected" : ""}>${i === 0 ? "Последний · " : ""}${esc(revisionStamp(r.created_at))} · #${r.id}</option>`).join("")}</select><button data-action="revision-step" data-step="-1" ${index === 0 ? "disabled" : ""} aria-label="Следующий снимок">${icon("right")}</button></div>
    <p class="revision-comparison">${new Date(rev.before_at).getTime() ? esc(revisionStamp(rev.before_at)) : "До правки"} <span aria-label="по сравнению с">→</span> <strong>${esc(revisionStamp(rev.created_at))}</strong> <span>МСК · время обнаружения</span></p>
    <div class="revision-legend"><span class="revision-added">+ ${plus} добавлено</span><span class="revision-removed">− ${minus} убрано</span><span>= ${unchanged} без изменений</span></div>
    <p class="revision-help">${plus || minus ? "Изменённая пара: − как было, + как стало. Остальные — без изменений." : "Видимых отличий нет: в этой версии могли обновиться другая подгруппа или служебные номера записей."} Снимки хранятся 14 дней.${new Date(rev.before_at).getTime() === 0 ? " Этот переход восстановлен из прежнего хранилища; время предыдущей версии неизвестно." : ""}</p>
    ${state.revisionOffline ? '<p class="notice">Офлайн: сохранённая история может быть неполной.</p>' : ""}
    ${index > 0 ? button("revision-latest", "К последнему снимку", "refresh") : ""}
    </details>
  </section>`;
}
function revisionSchedule() {
  if (
    !state.revision ||
    state.revisionLoading ||
    state.revisionError ||
    state.revisionContext !== revisionContext()
  )
    return "";
  const rows = revisionRows(state.revision, state.data?.grid),
    dates = Array.from({ length: 7 }, (_, i) => shift(state.week, i)),
    visibleDates = dates.filter(
      (d, i) => i < 6 || d === today() || rows.some((l) => l.date === d),
    ),
    changed = (d) =>
      rows.filter((l) => l.date === d && l.revisionKind !== "unchanged").length,
    cards = (d) => {
      const lessons = rows.filter((l) => l.date === d && (!state.revisionOnlyChanges || l.revisionKind !== "unchanged"));
      return lessons.length
        ? lessons.map((l) => lessonCard(l, state.view === "day")).join("")
        : `<div class="free-day">${state.revisionOnlyChanges ? "Изменений нет" : "Занятий в снимке нет"}</div>`;
    };
  if (state.view === "day")
    return `<div class="day-tabs revision-day-tabs" aria-label="День недели">${visibleDates.map((d) => `<button data-action="day" data-date="${d}" class="${[state.date === d ? "active" : "", d === today() ? "today" : ""].filter(Boolean).join(" ")}" aria-pressed="${state.date === d}" ${d === today() ? 'aria-current="date"' : ""}>${dateLabel(d, { weekday: "short" })}<strong>${dateLabel(d, { day: "numeric" })}</strong>${d === today() ? '<span class="today-label">Сегодня</span>' : ""}${changed(d) ? '<i class="edited-dot"></i>' : ""}</button>`).join("")}</div><div class="revision-day-list">${cards(visibleDates.includes(state.date) ? state.date : visibleDates[0])}</div>`;
  return `<div class="schedule-scroll revision-schedule" tabindex="0" aria-label="Снимок расписания с правками"><div class="week-grid ${visibleDates.length === 7 ? "seven" : ""}">${visibleDates.map((d) => `<section class="day-column"><div class="day-heading"><span>${dateLabel(d, { weekday: "short" })}</span><strong>${dateLabel(d, { day: "numeric" })}</strong>${changed(d) ? `<small class="revision-day-count">${changed(d)} ${plural(changed(d), "правка", "правки", "правок")}</small>` : '<small class="revision-day-count">Без правок</small>'}</div>${cards(d)}</section>`).join("")}</div></div>`;
}

function scheduleView() {
  const st = statistics(state.data.week);
  return (
    heading(
      "Всё по расписанию",
      "Пары, планы и время между ними.",
      groupControls(),
    ) +
    notice(state.data) +
    revisionsToggle() +
    (state.revisionMode ? "" : sessionBanner()) +
    `${state.revisionMode ? "" : state.week === monday(today()) ? myDayView() : `<div class="summary-row">${nextCard()}<article class="summary-card"><div class="card-top"><span>Пар на неделе</span>${icon("book")}</div><div class="metric">${st.count} <small>занятий</small></div><p class="caption">${st.studyDays} учебных дней по расписанию</p></article><article class="summary-card"><div class="card-top"><span>Учебная нагрузка</span>${icon("clock")}</div><div class="metric">${(st.minutes / 60).toLocaleString("ru", { maximumFractionDigits: 1 })} <small>часа</small></div><p class="caption">Без перемен и пересечений</p></article></div>`}<div class="content-layout"><section class="schedule-area">${weekControls(`<div class="segmented" aria-label="Вид расписания"><button data-action="view" data-view="week" class="${state.view === "week" ? "active" : ""}" aria-pressed="${state.view === "week"}">Неделя</button><button data-action="view" data-view="day" class="${state.view === "day" ? "active" : ""}" aria-pressed="${state.view === "day"}">День</button></div>`)}${state.revisionMode ? revisionsPanel() : ""}${state.revisionMode && (state.revision || state.revisionLoading || state.revisionError || state.revisionContext !== revisionContext()) ? revisionSchedule() : state.view === "week" ? weekGrid(state.data.week, state.data.grid) : dayList(state.data.week, state.data.grid)}</section><aside class="rail">${miniCalendar()}<section class="panel meet-panel">${icon("compare")}<h2>Пересечёмся?</h2><p>Разные группы — не повод не видеться. Узнайте, когда вы в одном корпусе, и пересекитесь на перемене.</p>${button("compare", "Сравнить расписания", "up")}</section>${freshness()}</aside></div>`
  );
}

// Экран сессии показывается, только когда сессия есть: вне её вуз не заводит
// ни экзаменов, ни зачётов, и блок исчезает целиком, а не висит пустым.
// Русские названия дней недели Intl отдаёт со строчной буквы, а заголовку она
// нужна прописной. Через CSS не выйдет: capitalize задел бы и подпись рядом.
const capitalize = (text) =>
  text.charAt(0).toLocaleUpperCase("ru") + text.slice(1);
function sessionBanner() {
  const items = state.exams || [];
  if (!items.length) return "";
  const next = items[0],
    left = daysUntil(next.date),
    when =
      left <= 0
        ? "сегодня"
        : left === 1
          ? "завтра"
          : `через ${left} ${plural(left, "день", "дня", "дней")}`;
  const kinds = new Map();
  for (const l of items)
    kinds.set(
      sessionKind(l.class_type),
      (kinds.get(sessionKind(l.class_type)) || 0) + 1,
    );
  const counts = [...kinds]
    .map(([kind, n]) => `<span>${esc(kind)}: ${n}</span>`)
    .join("");
  return `<section class="session-banner" aria-label="Сессия"><div>${icon("book")}<div><h2>Сессия</h2><p><strong>${esc(next.discipline || "Испытание")}</strong> — ${when}, ${dateLabel(next.date, { day: "numeric", month: "long" })}${next.minute_to > next.minute_from ? ` в ${clock(next.minute_from)}` : ""}. Дальше ещё ${items.length - 1}.</p><div class="change-counts">${counts}</div></div></div><div class="inline-actions">${button("session-open", `Вся сессия · ${items.length}`, "right")}</div></section>`;
}
function showSession() {
  const items = state.exams || [];
  if (!items.length) return;
  const byDate = new Map();
  for (const l of items) {
    if (!byDate.has(l.date)) byDate.set(l.date, []);
    byDate.get(l.date).push(l);
  }
  const blocks = [...byDate]
    .map(([date, lessons]) => {
      const left = daysUntil(date);
      return `<section class="session-day"><h3>${capitalize(dateLabel(date, { weekday: "long", day: "numeric", month: "long" }))}<span class="muted small">${left <= 0 ? "сегодня" : left === 1 ? "завтра" : `через ${left} дн.`}</span></h3>${lessons
        .map(
          (l) =>
            `<article class="session-item"><span class="session-mark">${esc(sessionKind(l.class_type))}</span><strong>${esc(l.discipline || "Испытание")}</strong><p class="small muted">${l.minute_to > l.minute_from ? `${clock(l.minute_from)} – ${clock(l.minute_to)} · ` : ""}${esc(placeLabel(l))}${(l.staff || []).length ? " · " + esc(l.staff.join(", ")) : ""}</p></article>`,
        )
        .join("")}</section>`;
    })
    .join("");
  detail(
    "Сессия",
    `<p class="small muted">${esc(state.groupInfo?.name || "")} · всё, что вуз завёл в расписании на полгода вперёд. Даты меняются — сверяйтесь перед каждым испытанием.</p>${blocks}${button("close-detail", "Понятно", "", "primary")}`,
  );
}
function teacherSearchParams() {
  return { q: state.teacherQuery, date: state.week,
    ...(state.teacherScope === "mine" && state.group ? { group: state.group } : {}) };
}
function teacherInitials(name) {
  return esc(name.split(/\s+/).filter(Boolean).slice(0, 2).map(s => s[0]).join(""));
}
function teacherPeriod(profile) {
  const [from, to] = profile?.from && profile?.to ? [profile.from, profile.to] : semesterOf(state.week);
  return `${dateLabel(from, { day: "numeric", month: "short", year: "numeric" })} — ${dateLabel(to, { day: "numeric", month: "short", year: "numeric" })}`;
}
function teacherGroups(groups = []) {
  return groups.map(g => `<span class="teacher-group ${g.id === state.group ? "mine" : ""}">${esc(g.name)}${g.id === state.group ? ' <span>(моя группа)</span>' : ""}</span>`).join("");
}
function teacherLesson(l) {
  const kind = lessonKind(l.class_type);
  return `<button class="teacher-lesson" data-action="lesson" data-id="${l.id}" data-date="${l.date}"><span class="teacher-time">${l.minute_to > l.minute_from ? `${clock(l.minute_from)}<small>${clock(l.minute_to)}</small>` : '—<small>Без времени</small>'}</span><span class="teacher-lesson-body"><span class="teacher-kind ${kind.key}">${esc(kind.label)}${l.flags & 2 ? " · Самоподготовка" : ""}</span><strong>${esc(l.discipline || "Занятие")}</strong><span class="teacher-lesson-place">${icon("pin")}${esc(placeLabel(l))}</span><span class="teacher-groups">${teacherGroups(l.groups)}${l.subgroup_id || !l.groups?.length ? `<span class="small muted">${esc(l.audience_label || "Группа не указана")}</span>` : ""}</span></span>${icon("right")}</button>`;
}
function teacherIdentity(t) {
  return [t.degree, ...(t.departments || [])].filter(Boolean).map(value => `<span class="teacher-identity">${esc(value)}</span>`).join("");
}
function teachersView() {
  if (state.teacher && !state.teacherData)
    return heading("Преподаватель", "Загружаем профиль и расписание…") + '<p class="progress-text" role="status">Собираем занятия</p>';
  if (state.teacherData) {
    const t = state.teacherData, profile = t.profile, subjects = profile?.subjects || [],
      sum = teacherWeekSummary(t.week, state.teacherSubject),
      next = sum.next, allLessons = items(t.week),
      subjectNames = [...new Set([...subjects.map(s => s.name), ...allLessons.map(l => l.discipline).filter(Boolean)])],
      selectedNames = state.teacherSubject && !subjectNames.includes(state.teacherSubject) ? [...subjectNames, state.teacherSubject] : subjectNames;
    const subjectCards = selectedNames.map(name => {
      const subject = subjects.find(s => s.name === name), n = allLessons.filter(l => l.discipline === name).length;
      const tag = selectedNames.length > 1 || state.teacherSubject ? "button" : "article";
      return `<${tag} class="teacher-subject ${state.teacherSubject === name ? "active" : ""}" ${tag === "button" ? `data-action="teacher-subject" data-name="${esc(name)}" aria-pressed="${state.teacherSubject === name}"` : ""}><span class="teacher-subject-title">${icon("book")}<strong>${esc(name)}</strong></span>${subject?.kinds?.length ? `<span class="small muted">${esc([...new Set(subject.kinds.map(k => lessonKind(k).label))].join(" · "))}</span>` : ""}<span class="teacher-groups">${teacherGroups(subject?.groups)}</span><span class="teacher-subject-count">${n ? `${n} ${plural(n, "занятие", "занятия", "занятий")} на неделе` : "На этой неделе занятий нет"}</span></${tag}>`;
    }).join("");
    const nextCard = next ? `<button class="teacher-next" data-action="lesson" data-id="${next.id}" data-date="${next.date}"><span class="eyebrow">${sum.current ? "Сейчас по расписанию" : "Ближайшее на этой неделе"}</span><strong>${esc(next.discipline || "Занятие")}</strong><span>${dateLabel(next.date, { weekday: "short", day: "numeric", month: "short" })} · ${clock(next.minute_from)} – ${clock(next.minute_to)} МСК</span><span>${esc(placeLabel(next))}</span><span class="teacher-groups">${teacherGroups(next.groups)}</span></button>` : "";
    const agenda = sum.lessons.length ? (t.week.days || []).map(day => {
      const lessons = sum.lessons.filter(l => l.date === day.date);
      return `<section class="teacher-day"><div class="teacher-day-heading"><h3>${dateLabel(day.date, { weekday: "long", day: "numeric", month: "short" })}</h3>${day.date === today() ? '<span class="teacher-today">Сегодня</span>' : ""}<span class="small muted">${lessons.length ? `${lessons.length} ${plural(lessons.length, "занятие", "занятия", "занятий")}` : "Нет занятий в загруженных данных"}</span></div>${lessons.map(teacherLesson).join("")}</section>`;
    }).join("") : empty(state.teacherSubject ? "По предмету нет занятий на этой неделе" : "На этой неделе занятий не найдено", "Посмотрите соседнюю неделю. Пустое расписание не означает, что преподаватель свободен.", state.teacherSubject ? button("teacher-subject", "Все предметы", "book") : "");
    const rooms = sum.rooms.length ? `<section class="panel teacher-places"><h2>Где проходят занятия</h2><p class="small muted">${state.teacherSubject ? "По выбранному предмету на неделе" : "В открытой неделе"}. Аудитория может меняться.</p>${sum.rooms.map(r => `<div>${icon("pin")}<span>${esc(r.name)}</span><small>${r.count} ${plural(r.count, "занятие", "занятия", "занятий")}</small></div>`).join("")}</section>` : "";
    return `<div class="teacher-back">${button("all-teachers", "Преподаватели", "left")}${button("share", "Поделиться", "share")}</div>` +
      `<div class="teacher-heading"><span class="avatar">${teacherInitials(t.teacher.name)}</span><div><span class="eyebrow muted">Преподаватель</span><h1>${esc(t.teacher.full_name || t.teacher.name)}</h1>${teacherIdentity(t.teacher)}<p>${subjects.length === 1 ? esc(subjects[0].name) : subjects.length ? `${subjects.length} ${plural(subjects.length, "предмет", "предмета", "предметов")} в загруженном расписании полугодия` : "Предметы появятся после загрузки занятий"}</p></div></div>` +
      notice(t) + `<div class="teacher-layout"><aside class="teacher-aside"><details id="teacher-profile" class="panel teacher-subjects" ${(state.teacherProfileOpen ?? !matchMedia("(max-width: 800px)").matches) ? "open" : ""}><summary>Что ведёт <span>${selectedNames.length} ${plural(selectedNames.length, "предмет", "предмета", "предметов")}</span></summary><p class="small muted">${teacherPeriod(profile)}</p>${selectedNames.length > 1 ? '<p class="small muted">Выберите предмет, чтобы отфильтровать неделю.</p>' : ""}${selectedNames.length ? `${selectedNames.length > 1 || state.teacherSubject ? `<button class="teacher-subject-all ${!state.teacherSubject ? "active" : ""}" data-action="teacher-subject" aria-pressed="${!state.teacherSubject}">Все предметы <span>${allLessons.length}</span></button>` : ""}${subjectCards}` : '<p class="small muted">В этом полугодии предметов пока нет в загруженных данных.</p>'}<p class="teacher-source small muted">${profile?.missing ? "Полугодие загружено частично. " : ""}${profile?.stale ? "Сведения о предметах давно не обновлялись. " : ""}Предметы и группы определены по расписанию; это не учебный план.</p></details>${rooms}</aside><div class="teacher-main">${nextCard}<section class="teacher-schedule"><div class="teacher-schedule-title"><h2>Учебная неделя</h2><span class="small muted">Время МСК</span></div>${weekControls()}${state.teacherSubject ? `<div class="teacher-filter"><span>${esc(state.teacherSubject)}</span>${button("teacher-subject", "Сбросить", "close")}</div>` : ""}<div class="teacher-week-stats"><span><strong>${sum.lessons.length}</strong> ${plural(sum.lessons.length, "занятие", "занятия", "занятий")}</span><span><strong>${sum.days}</strong> ${plural(sum.days, "учебный день", "учебных дня", "учебных дней")}</span><span><strong>${sum.groups.length}</strong> ${plural(sum.groups.length, "группа", "группы", "групп")}</span></div>${agenda}</section><p class="teacher-source small muted">Показаны занятия по загруженным группам. Потоковая пара учитывается один раз. Отсутствие пары не гарантирует, что преподаватель свободен.</p>${freshness(t)}</div></div>`;
  }
  return heading("Преподаватели", "Найдите человека по фамилии или предмету — узнайте, что он ведёт и где проходят занятия.") +
    `<section class="teacher-search-panel"><label class="search-field">${icon("search")}<input id="teacher-search" type="search" value="${esc(state.teacherQuery)}" placeholder="Фамилия или предмет" aria-label="Поиск по фамилии или предмету" autocomplete="off"></label><div class="teacher-search-meta"><div class="segmented" role="group" aria-label="Каких преподавателей показать"><button data-action="teacher-scope" data-scope="all" class="${state.teacherScope === "all" ? "active" : ""}" aria-pressed="${state.teacherScope === "all"}">Все преподаватели</button>${state.group ? `<button data-action="teacher-scope" data-scope="mine" class="${state.teacherScope === "mine" ? "active" : ""}" aria-pressed="${state.teacherScope === "mine"}">Моей группы${state.groupInfo?.name ? ` · ${esc(state.groupInfo.name)}` : ""}</button>` : ""}</div><span class="small muted">${teacherPeriod(state.teacherCatalog)}</span></div></section><div id="teacher-results" aria-live="polite" aria-busy="${state.loading}">${state.loading ? '<p class="progress-text">Ищем преподавателей…</p>' : teacherCards()}</div>`;
}
function teacherCards() {
  const hint = '<p class="small muted teacher-source">По загруженному расписанию выбранного полугодия. До 50 результатов — уточните фамилию или предмет, если нужного человека нет.</p>';
  const offline = state.teacherCatalog?.offline ? '<p class="notice warning">Поиск недоступен. Показаны сохранённые результаты, сведения могли измениться.</p>' : "";
  return offline + (state.teacherResults.length
    ? `<p class="small muted teacher-result-count">${state.teacherResults.length === 50 ? "Первые 50 преподавателей" : `${state.teacherResults.length} ${plural(state.teacherResults.length, "преподаватель", "преподавателя", "преподавателей")}`}</p><div class="teacher-list">${state.teacherResults.map(t => `<button class="teacher-card" data-action="teacher" data-id="${t.id}"><span class="avatar">${teacherInitials(t.name)}</span><span class="teacher-card-body"><strong>${esc(t.full_name || t.name)}</strong>${teacherIdentity(t)}<span class="teacher-card-subjects">${(t.subjects || []).slice(0, 2).map(name => `<span>${esc(name)}</span>`).join("") || '<span>Предмет не указан</span>'}${t.subjects?.length > 2 ? `<span>Ещё ${t.subjects.length - 2}</span>` : ""}</span>${t.groups?.length ? `<span class="teacher-card-context">${esc(t.groups.slice(0, 3).join(", "))}${t.groups.length > 3 ? ` · ещё ${t.groups.length - 3}` : ""}</span>` : ""}<small>Профиль и расписание ${icon("right")}</small></span></button>`).join("")}</div>${hint}`
    : empty("Преподаватели не найдены", state.teacherScope === "mine" ? "В загруженном расписании вашей группы совпадений нет. Попробуйте другой запрос или посмотрите всех преподавателей." : "Попробуйте другую фамилию или предмет. Преподаватель появится здесь, когда будут загружены его занятия за это полугодие.", state.teacherScope === "mine" ? button("teacher-scope", "Все преподаватели", "users", "", 'data-scope="all"') : "") + hint);
}
function statsView() {
  const st = statistics(state.data.week),
    max = Math.max(1, ...st.days.map((d) => d.minutes));
  return (
    heading(
      "Неделя в цифрах",
      "Понимайте нагрузку и планируйте силы.",
      groupControls(),
    ) +
    notice(state.data) +
    weekControls() +
    `<div class="stat-grid">${[
      ["Занятий", st.count, state.subgroup ? "По выбранной подгруппе" : "Занятия всех подгрупп"],
      [
        "Часов занятий",
        (st.minutes / 60).toLocaleString("ru", { maximumFractionDigits: 1 }),
        "По 60 минут, без наложений",
      ],
      ["Дисциплин", st.subjects.length, "На этой неделе"],
      [
        "Время в окнах",
        (st.gapMinutes / 60).toLocaleString("ru", {
          maximumFractionDigits: 1,
        }) + " ч",
        "Перерывы от 40 минут",
      ],
    ]
      .map(
        ([label, n, caption], i) =>
          `<article class="summary-card ${i === 0 ? "featured" : ""}"><div class="card-top">${label}</div><div class="metric">${n}</div><p class="caption">${caption}</p></article>`,
      )
      .join(
        "",
      )}</div><div class="section-grid"><section class="panel"><h2>Нагрузка по дням</h2><p class="small muted">Часы занятий, без перемен</p><div class="bar-chart" role="img" aria-label="${st.days.map((d) => dateLabel(d.date, { weekday: "long" }) + ": " + d.minutes + " минут").join("; ")}">${st.days.map((d) => `<div class="bar-column"><strong>${(d.minutes / 60).toLocaleString("ru", { maximumFractionDigits: 1 })}</strong><div class="bar" style="height:${Math.round((d.minutes / max) * 130)}px"></div><span>${dateLabel(d.date, { weekday: "short" })}</span></div>`).join("")}</div><p class="small muted" style="margin-top:18px">${st.count ? "Самый насыщенный день — " + dateLabel([...st.days].sort((a, b) => b.minutes - a.minutes)[0].date, { weekday: "long" }) + "." : "Занятий в выбранной неделе нет."}</p></section><section class="panel"><h2>На что уходит неделя</h2>${st.subjects.length ? st.subjects.map(([name, n]) => `<button class="subject-row" data-action="subject" data-name="${esc(name)}"><span>${esc(name)}</span><strong>${n}</strong><div class="track"><span style="width:${(n / st.count) * 100}%"></span></div></button>`).join("") : '<p class="muted">Дисциплины появятся вместе с расписанием.</p>'}</section><section class="panel"><h2>Формат занятий</h2>${st.types.map(([type, n]) => `<div class="subject-row"><span>${esc(type)}</span><strong>${n} · ${Math.round((n / st.count) * 100)}%</strong></div>`).join("") || '<p class="muted">Пока нет данных</p>'}</section><section class="panel meet-panel">${icon("coffee")}<h2>Окна тоже можно планировать</h2><p>Найдите свободное время с другой группой: для обеда, подготовки или просто встречи.</p>${button("compare", "Найти общее время", "up")}</section></div><p class="small muted" style="margin-top:20px">${state.subgroup ? "Статистика выбранной подгруппы." : "Выбраны все подгруппы: параллельные занятия считаются отдельно, их время не суммируется дважды."} Пустые слоты и неучебные дни исключены.</p>`
  );
}
function proximityLabel(match) {
  if (match.sharedLesson) return "Общая пара";
  if (match.kind === "room") return "Одна аудитория";
  if (match.kind === "floor")
    return `${match.floorInferred ? "Вероятно, " : ""}${match.floor}-й этаж`;
  return "Один корпус";
}
function proximityCard(match, isBreak, verified) {
  const lesson = (l, name) =>
    `<div class="proximity-lesson"><span class="small muted">${esc(name)}${l.subgroup_id ? " · " + esc(l.audience_label || "Подгруппа") : ""} · ${clock(l.minute_from)}–${clock(l.minute_to)}</span><strong>${esc(l.discipline)}</strong><span class="proximity-room">${icon("pin")}${esc(l.classroom)}</span></div>`;
  const endLabel = isBreak
    ? `${match.to - match.from} мин после пар`
    : match.timing === "handoff"
      ? "соседние пары по времени"
      : "пары идут одновременно";
  const alternatives = match.alternatives || [match];
  const evidence = side => [...new Map(alternatives.map(m => [m[side].id, m[side]])).values()].map(l => lesson(l, side === "a" ? state.groupInfo.name : state.otherInfo.name)).join("");
  const pair = match.sharedLesson ? lesson(match.a, `${state.groupInfo.name} и ${state.otherInfo.name}`) : evidence("a") + evidence("b");
  return `<article class="proximity-card${match.sharedLesson ? " compact" : ""}"><div class="proximity-heading"><span class="proximity-badge ${match.kind}">${icon(isBreak ? "coffee" : "pin")}${proximityLabel(match)}</span><span class="small muted">Корпус ${esc(match.building)}</span></div><div class="proximity-time"><strong>${clock(match.from)}${match.from === match.to ? "" : " – " + clock(match.to)}</strong><span>${endLabel}</span></div><div class="proximity-pair">${pair}</div>${isBreak ? `<div class="proximity-footer"><span class="small muted">Можно договориться о встрече у аудиторий.</span>${button("meeting", "Выбрать время", "right", "", `data-date="${match.a.date}" data-from="${match.from}" data-to="${match.to}"`)}</div>` : `<p class="proximity-note">${verified ? (match.timing === "handoff" ? "Одна группа выходит, другая приходит. Это подсказка о месте; занятость обоих в промежутке не проверялась." : "В это время вы на занятиях. Для встречи посмотрите «Перемены рядом».") : "По имеющимся данным. Совпадение места может быть неактуально."}</p>`}</article>`;
}
function comparisonTimeline(ls, name) {
  return `<div class="timeline-row"><span>${esc(name)}</span><div class="timeline" role="img" aria-label="${esc(name)}: ${
    ls
      .filter(actual)
      .map(
        (l) =>
          clock(l.minute_from) +
          "–" +
          clock(l.minute_to) +
          " " +
          esc(l.discipline),
      )
      .join("; ") || "Занятий нет в расписании"
  }">${intervals(ls, 510, state.meetingEnd)
    .map(
      ([s, e]) =>
        `<span class="time-block" style="left:${((s - 510) / (state.meetingEnd - 510)) * 100}%;width:${((e - s) / (state.meetingEnd - 510)) * 100}%"></span>`,
    )
    .join("")}</div></div>`;
}
function compareView() {
  const verified =
    state.other &&
    !state.data.missing &&
    !state.other.missing &&
    !state.data.stale &&
    !state.other.stale &&
    !state.data.offline &&
    !state.other.offline;
  const mode = state.compareMode;
  const nearFilter = (match) =>
    state.proximity === "all" ||
    (state.proximity === "room"
      ? match.kind === "room"
      : match.kind !== "building");
  const days = state.other
    ? Array.from({ length: 7 }, (_, i) => {
        const date = shift(state.week, i),
          a = state.data.week.days.find((d) => d.date === date)?.items || [],
          b = state.other.week.days.find((d) => d.date === date)?.items || [];
        return {
          date,
          a,
          b,
          nearby: groupedNearby(nearbyLessons(a, b).filter(nearFilter)),
          breaks: verified ? nearbyBreaks(a, b).filter(nearFilter) : [],
          free: verified ? freeTogether(a, b, { min: state.minMeeting, to: state.meetingEnd }) : [],
        };
      })
    : [];
  const past = (date, end) => date < today() || (date === today() && end <= nowMinute());
  const futureDays = days.filter(d => d.date >= today()).map(d => ({ ...d,
    nearby: d.nearby.filter(m => !past(d.date, m.to)),
    breaks: d.breaks.filter(m => !past(d.date, m.to)),
    free: d.free.filter(([, end]) => !past(d.date, end)).map(([from, to]) => [d.date === today() ? Math.max(from, nowMinute()) : from, to]).filter(([from, to]) => to - from >= state.minMeeting),
  }));
  const pastDays = days.map(d => ({ ...d,
    nearby: d.nearby.filter(m => past(d.date, m.to)),
    breaks: d.breaks.filter(m => past(d.date, m.to)),
    free: d.free.filter(([, end]) => past(d.date, end)),
  })).filter(d => d.date < today() || d[mode].length);
  const modeOptions = [
    ["nearby", "pin", "Рядом на парах", "Один корпус, этаж или аудитория"],
    ["breaks", "coffee", "Перемены рядом", "Даже пяти минут хватит на встречу"],
    ["free", "clock", "Свободное время", "Общие окна и время после учёбы"],
  ];
  const modeSwitch = state.other
    ? `<div class="comparison-modes" aria-label="Что ищем">${modeOptions.map(([key, ico, label, description]) => `<button data-action="compare-mode" data-mode="${key}" class="${mode === key ? "active" : ""}" aria-pressed="${mode === key}"><span>${icon(ico)}${label}<strong>${!verified && key !== "nearby" ? "—" : futureDays.reduce((n, d) => n + d[key].length, 0)}</strong></span><small>${description}</small></button>`).join("")}</div>`
    : "";
  const filter =
    mode === "free"
      ? `<label class="comparison-filter">Встреча от<select id="meeting-min">${[15, 30, 60, 90].map((n) => `<option value="${n}" ${state.minMeeting === n ? "selected" : ""}>${n} минут</option>`).join("")}</select></label><label class="comparison-filter">Искать до<select id="meeting-end">${[1080, 1200, 1320].map(n => `<option value="${n}" ${state.meetingEnd === n ? "selected" : ""}>${clock(n)}</option>`).join("")}</select></label>`
      : `<label class="comparison-filter">Где пересекаемся<select id="proximity-filter">${[
          ["all", "В одном корпусе"],
          ["floor", "На одном этаже или ближе"],
          ["room", "В одной аудитории"],
        ]
          .map(
            ([value, label]) =>
              `<option value="${value}" ${state.proximity === value ? "selected" : ""}>${label}</option>`,
          )
          .join("")}</select></label>`;
  let results = "";
  if (state.other) {
    const notes =
      mode === "nearby"
        ? "Показываем одновременные пары и соседние по времени — с разрывом до 30 минут. Этаж с пометкой «вероятно» предполагается по трёхзначному номеру аудитории. Это не план здания."
        : mode === "breaks"
          ? "Ищем от 5 минут сразу после очных пар в одном корпусе. Проверяем, что оба свободны, и ограничиваем подсказку 30 минутами после более раннего окончания."
          : `Ищем общее свободное время с 08:30 до ${clock(state.meetingEnd)} МСК. Расположение групп не учитывается — дорогу между корпусами запланируйте отдельно.`;
    results = `<p class="comparison-explanation">Сначала — предстоящие варианты. ${notes}</p>`;
    if (!verified)
      results +=
        '<div class="notice warning">Данные неполные или устарели. Можно сопоставить известные места занятий, но свободное время и перемены пока не подтверждены.</div>';
    if (
      state.group === state.compare &&
      state.subgroup === state.compareSubgroup
    )
      results +=
        '<div class="notice">Вы сравниваете одну и ту же группу и подгруппу. Выберите группу друга.</div>';
    const visibleDays =
      mode === "free" ? futureDays : futureDays.filter((d) => d[mode].length);
    const renderDays = list => list.map(
        (d) =>
          `<section class="meet-day"><div class="meet-day-heading"><h2>${dateLabel(d.date, { weekday: "long", day: "numeric", month: "long" })}</h2><span>${d.a.filter(actual).length} и ${d.b.filter(actual).length} занятий</span></div>${mode === "free" ? `<div class="timeline-axis">${[510, 720, 900, ...(state.meetingEnd > 1080 ? [1080] : []), state.meetingEnd].map(n => `<span style="left:${(n - 510) / (state.meetingEnd - 510) * 100}%">${clock(n)}</span>`).join("")}</div>${comparisonTimeline(d.a, state.groupInfo.name)}${comparisonTimeline(d.b, state.otherInfo.name)}<div class="meeting-slots">${d.free.map(([s, e]) => `<button class="meeting-slot" data-action="meeting" data-date="${d.date}" data-from="${s}" data-to="${e}">${icon("coffee")} ${clock(s)} – ${clock(e)} <small>${e - s} мин</small></button>`).join("") || `<span class="small muted">${verified ? "Подходящего свободного времени не найдено." : "Свободное время пока не подтверждено."}</span>`}</div>` : `<div class="proximity-list">${d[mode].map((match) => proximityCard(match, mode === "breaks", verified)).join("")}</div>`}</section>`,
      )
      .join("");
    results += renderDays(visibleDays);
    if (pastDays.length) results += `<details class="comparison-past" ${state.week < monday(today()) ? "open" : ""}><summary>Прошедшие дни и варианты · ${pastDays.length}</summary>${renderDays(pastDays)}</details>`;
    if (!visibleDays.length)
      results += empty(
        mode === "breaks"
          ? "Перемен рядом пока не нашли"
          : "Совпадений места пока нет",
        mode === "breaks"
          ? "Попробуйте другую неделю или посмотрите, когда пары проходят рядом. Подсказки о переменах требуют свежего расписания обеих групп."
          : "Для совпадения нужны близкие по времени очные пары и узнаваемый корпус в обеих аудиториях. Можно ослабить фильтр или выбрать другую неделю.",
      );
  }
  return (
    heading(
      "Давайте пересечёмся",
      "На одном этаже, на перемене или после пар.",
    ) +
    `<div class="comparison-controls">${groupSwitch()}${icon("compare")}${groupSwitch("compare")}<label>Моя подгруппа<select id="subgroup">${options(state.subgroups, state.subgroup)}</select></label><label>Подгруппа друга<select id="compare-subgroup">${options(state.otherSubgroups, state.compareSubgroup)}</select></label></div>` +
    notice(state.data) +
    notice(state.other) +
    modeSwitch +
    weekControls(state.other ? filter : "") +
    (state.other
      ? results
      : empty(
          "С кем встречаемся?",
          "Выберите группу друга. Найдём соседние аудитории, перемены рядом и общее свободное время.",
          button(
            "pick",
            "Выбрать группу друга",
            "users",
            "primary",
            'data-target="compare"',
          ),
        ))
  );
}
// Прошла ли пара. Не «наступил ли её день»: «осталось три занятия» в перерыве
// между парами — неправда.
function lessonPast(l) {
  return (
    l.date < today() ||
    (l.date === today() && l.minute_to > l.minute_from && l.minute_to <= nowMinute())
  );
}
function subjectRow(l) {
  const kind = lessonKind(l.class_type),
    place = placeLabel(l),
    who = (l.staff || []).map(shortName).join(", ");
  return `<button class="subject-lesson ${kind.key} ${lessonPast(l) ? "past" : ""}" data-action="lesson" data-id="${l.id}" data-date="${l.date}"><span class="subject-when"><strong>${dateLabel(l.date, { day: "numeric", month: "short" })}</strong><small>${dateLabel(l.date, { weekday: "short" })}</small></span><span class="subject-what"><strong>${l.minute_to > l.minute_from ? `${clock(l.minute_from)} – ${clock(l.minute_to)}` : "Время не указано"}</strong><small>${esc(place)}${who ? " · " + esc(who) : ""}${l.subgroup_id ? " · " + esc(l.audience_label || "Подгруппа") : ""}</small>${l.topic ? `<em>${esc(l.topic)}</em>` : ""}</span><span class="lesson-type">${esc(kind.label)}</span></button>`;
}
function subjectView() {
  const back = button("schedule-back", "К расписанию", "left");
  const d = state.subjectData;
  if (!d || d.failed)
    return heading(esc(state.subjectName || "Про предмет"), "", back) +
      (d?.failed ? empty(
        d.notFound ? "Предмет не найден" : "Не удалось загрузить предмет",
        d.notFound ? "Такого предмета нет в справочнике. Вернитесь к расписанию и откройте его из занятия." : "Сервер временно недоступен. Попробуйте загрузить ещё раз.",
        button("reload", "Повторить", "refresh", "primary"),
      ) : '<p class="progress-text" role="status">Загружаем занятия по предмету…</p>');
  const sum = subjectSummary(d.items, today(), nowMinute());
  const name = d.discipline?.name || state.subjectName;
  const period = `${dateLabel(d.from, { day: "numeric", month: "long", year: "numeric" })} — ${dateLabel(d.to, { day: "numeric", month: "long", year: "numeric" })}`;
  const complete = !d.missing && d.months > 0 && d.months_loaded >= d.months;
  const coverage = `<div class="subject-coverage">${icon("info")}<div><strong>По опубликованному расписанию</strong><p>${complete ? "Загружены все месяцы периода. Расписание ещё может измениться." : `Загружено месяцев: ${d.months_loaded || 0} из ${d.months || "—"}. Другие занятия могут появиться позже.`}${d.loaded_months?.length ? " Доступны: " + d.loaded_months.filter(m => /^\d{4}-\d{2}$/.test(m)).map(m => dateLabel(m + "-01", { month: "long" })).join(", ") + "." : ""}</p></div></div>`;
  const next = sum.next
    ? `<section class="panel subject-next"><div class="card-top"><span>${sum.current ? "Сейчас по расписанию" : "Ближайшее занятие"}</span>${icon("calendar")}</div>${subjectRow(sum.next)}${sum.next.comments ? `<p class="small muted">${esc(sum.next.comments)}</p>` : ""}</section>`
    : `<section class="subject-empty"><h2>${d.to < today() ? "Прошедший период" : "Новых занятий пока нет"}</h2><p class="muted">${d.to < today() ? "Занятия этого периода можно посмотреть во вкладке «Прошедшие»." : "В загруженном расписании нет предстоящих занятий по этому предмету."}</p></section>`;
  const controls = `<section class="panel subject-control"><h2>Контроль и консультации</h2>${sum.assessments.length
    ? sum.assessments.map(subjectRow).join("")
    : ''}</section>`;
  const teachers = sum.teachers.length ? sum.teachers.map(t =>
    `<button class="teacher-row" data-action="find-teacher" data-name="${esc(t.name)}"><span><strong>${esc(t.name)}</strong><small>${esc(t.kinds.join(", "))} · ${t.count} ${plural(t.count, "занятие", "занятия", "занятий")}</small></span>${icon("right")}</button>`,
  ).join("") : '<p class="muted">Преподаватели пока не указаны.</p>';
  const formats = sum.kinds.length
    ? `<div class="kind-bar" role="img" aria-label="${esc(sum.kinds.map(k => `${k.label}: ${k.count}`).join(", "))}">${sum.kinds.map(k => `<span class="${k.key || "other"}" style="width:${k.count / sum.total * 100}%"></span>`).join("")}</div><div class="kind-legend">${sum.kinds.map(k => `<span class="${k.key || "other"}"><i></i>${esc(k.label)} · ${k.count}</span>`).join("")}</div>`
    : '<p class="muted">Виды занятий пока не указаны.</p>';
  const rooms = sum.rooms.length ? `<div class="room-list">${sum.rooms.map(([place, n]) =>
    `<div class="subject-row"><span>${icon("pin")}${esc(place)}</span><strong>${n}</strong></div>`,
  ).join("")}</div>` : '<p class="muted">Места занятий пока не указаны.</p>';
  const filter = state.subjectFilter === "auto" ? (!sum.upcoming.length && sum.past.length ? "past" : "upcoming") : state.subjectFilter;
  const selected = filter === "all" ? sum.all : filter === "past" ? [...sum.past].reverse() : sum.upcoming;
  const months = new Map();
  for (const l of selected.slice(0, state.subjectLimit)) {
    const key = l.date.slice(0, 7);
    if (!months.has(key)) months.set(key, []);
    months.get(key).push(l);
  }
  const lessons = [...months].map(([key, ls]) => {
    const total = selected.filter(l => l.date.startsWith(key)).length;
    const count = ls.length < total ? `Показано ${ls.length} из ${total}` : `${total} ${plural(total, "занятие", "занятия", "занятий")}`;
    return `<section class="subject-month"><h3>${capitalize(dateLabel(key + "-01", { month: "long", year: "numeric" }))}<span class="muted small">${count}</span></h3>${ls.map(subjectRow).join("")}</section>`;
  }).join("") || empty(filter === "past" ? "Прошедших занятий нет" : filter === "all" ? "Занятия пока не опубликованы" : "Предстоящих занятий пока нет", "Показаны только занятия из загруженного расписания выбранной группы и подгруппы.");
  const filters = [["upcoming", "Предстоящие", sum.left], ["past", "Прошедшие", sum.done], ["all", "Все", sum.total]];
  return `<div class="subject-header">${heading(esc(name || "Про предмет"), period, back)}</div>` +
    `<div class="subject-context"><span class="subject-group">${icon("users")}${esc(state.groupInfo?.name || "Группа")}</span>${state.subgroups.length ? `<label class="subgroup-field"><small>Подгруппа</small><select id="subgroup" aria-label="Моя подгруппа">${options(state.subgroups, state.subgroup)}</select></label>` : ""}</div>` + notice(d) + coverage +
    `<div class="subject-layout"><div class="subject-main">${sum.next ? next : `<p class="subject-empty">${d.to < today() ? "Прошедший период." : "В загруженном расписании новых занятий пока нет."}${filter === "past" ? " Ниже — последние занятия." : ""}${!sum.assessments.length ? " Предстоящие контрольные мероприятия пока не указаны." : ""}</p>`}${sum.assessments.length ? controls : ""}<section class="subject-feed"><div class="subject-feed-head"><h2>Занятия по предмету</h2><div class="segmented subject-filters" role="group" aria-label="Какие занятия показать">${filters.map(([key, label, n]) => `<button data-action="subject-filter" data-filter="${key}" class="${key === filter ? "active" : ""}" aria-pressed="${key === filter}">${label} <span>${n}</span></button>`).join("")}</div></div>${lessons}${selected.length > state.subjectLimit ? button("subject-more", `Показать ещё · осталось ${selected.length - state.subjectLimit}`, "down") : ""}</section></div><aside class="subject-aside"><section class="panel"><h2>В расписании</h2><p class="small muted">Занятия выбранного периода, без учёта посещаемости.</p><div class="subject-totals"><strong>${sum.total}<small>${plural(sum.total, "занятие", "занятия", "занятий")}</small></strong><strong>${(sum.minutes / 60).toLocaleString("ru", { maximumFractionDigits: 1 })}<small>часов по 60 минут</small></strong></div>${!state.subgroup && state.subgroups.length ? '<p class="small muted">Занятия всех подгрупп считаются отдельно. Время параллельных занятий учтено один раз.</p>' : ""}${sum.unknownTime ? `<p class="small muted">Без длительности: ${sum.unknownTime}. Их время не входит в сумму.</p>` : ""}${formats}</section><section class="panel"><h2>Кто ведёт</h2>${teachers}</section><section class="panel"><h2>Где проходят занятия</h2>${rooms}</section>${freshness(d)}</aside></div>`;
}

function settingsView() {
  return (
    heading("Как удобно вам", "Сайт запомнит ваш выбор на этом устройстве.") +
    `<div class="settings-stack">${adminAccess ? `<section class="panel"><h2>Администрирование</h2><p class="small muted">Работа сервисов, пользователи и история метрик.</p><a class="button" href="/admin/">Открыть админку →</a></section>` : ""}${config.beta ? `<section class="panel"><h2>Закрытая бета</h2><p class="small muted">Вход сохранён на 60 дней. До 4 устройств для одного аккаунта бота.</p><a class="button" href="/account">Устройства и выход →</a></section>` : ""}<section class="panel settings-bots"><h2>Боты всегда рядом</h2><p class="small muted">Расписание и уведомления об изменениях — в привычном мессенджере.${state.groupInfo ? " Telegram-бот откроется с группой " + esc(state.groupInfo.name) + " — искать её заново не придётся." : ""}</p>${botsSection()}</section><section class="panel"><h2>Ваше расписание</h2><div class="setting-row"><div><strong>Группа и подгруппа</strong><p>${esc(state.groupInfo?.name || "Группа ещё не выбрана")}${state.subgroups.length ? " · " + esc(subgroupLabel()) : ""}</p></div>${button("pick", "Выбрать группу", "users", "", 'data-target="group"')}</div><div class="setting-row"><div><strong>Копии группы в каталоге</strong><p>Если расписание не то, проверьте одноимённые записи.</p></div>${button("twins", "Проверить копии", "", "", state.group ? "" : "disabled")}</div><div class="setting-row"><div><strong>Вид по умолчанию</strong><p>Сетка недели или список на день.</p></div><select id="default-view" aria-label="Вид по умолчанию"><option value="week" ${state.view === "week" ? "selected" : ""}>Неделя</option><option value="day" ${state.view === "day" ? "selected" : ""}>День</option></select></div><div class="setting-row"><div><strong>Переходы между корпусами</strong><p id="show-transfers-description">Подсказки между парами в разных корпусах. Полезно, если корпуса далеко друг от друга.</p></div><select id="show-transfers" aria-label="Переходы между корпусами" aria-describedby="show-transfers-description"><option value="off" ${state.showTransfers ? "" : "selected"}>Выключены</option><option value="on" ${state.showTransfers ? "selected" : ""}>Включены</option></select></div></section><section class="panel"><h2>Оформление и приложение</h2><div class="setting-row"><div><strong>Тема</strong><p>Системная следует настройкам устройства или Telegram. Звёздная ночь — уютное небо с рисованными звёздами.</p></div><select id="theme-select" aria-label="Тема оформления">${themes
      .map(
        ({ value: v, name: n }) =>
          `<option value="${v}" ${v === state.theme ? "selected" : ""}>${n}</option>`,
      )
      .join(
        "",
      )}</select></div><div class="setting-row"><div><strong>Всегда под рукой</strong><p>Добавьте сайт на главный экран телефона.</p></div>${button("install", "Установить", "phone")}</div><div class="setting-row"><div><strong>Расписание в календаре</strong><p>Скачать выбранную неделю. Файл не обновляется автоматически.</p></div>${button("export", "Скачать .ics", "download", "", state.group ? "" : "disabled")}</div></section><section class="panel"><h2>Избранные группы</h2><div class="favorite-list">${state.favorites.map((g) => `<div><button class="button" data-action="favorite-open" data-id="${g.id}">${esc(g.name)}</button>${ib("favorite-remove", "close", "Удалить " + g.name, 'data-id="' + g.id + '"')}</div>`).join("") || '<p class="small muted" style="margin-top:16px">Нажмите звёздочку в расписании, чтобы сохранить группу.</p>'}</div></section><section class="panel"><h2>Память устройства</h2><p class="small muted" style="margin-top:16px">Группа хранится в cookie на год. Подгруппа, тема, избранное и последние открытые расписания — в памяти браузера. Они не синхронизируются с ботом и другими устройствами. Рекламных cookie нет.</p><div class="setting-row"><div><strong>Сбросить настройки</strong><p>Удалить выбор группы, избранное и сохранённые копии.</p></div>${button("reset", "Сбросить")}</div></section><p class="small"><a href="/source">Исходный код и лицензия</a></p></div>`
  );
}

async function openPicker(target = "group") {
  pickerTarget = target;
  pickerItems = [];
  const g = ++pickerGeneration;
  const quickGroups = [...new Map([...(state.groupInfo ? [state.groupInfo] : []), ...state.favorites].map(g => [g.id, g])).values()];
  const quick = `<div class="picker-quick">${quickGroups.length ? `<p class="small muted">${target === "compare" ? "Быстрый выбор" : "Текущая и избранные"}</p><div class="inline-actions">${quickGroups.map(g => button("quick-group", esc(g.name), "", "", `data-id="${g.id}" data-target="${target}"`)).join("")}</div>` : ""}${target === "group" && state.subgroups.length ? `<label class="picker-subgroup">Подгруппа ${esc(state.groupInfo?.name)}<select id="picker-subgroup">${options(state.subgroups, state.subgroup)}</select></label>` : ""}${target === "group" && state.groupInfo && state.homeGroup?.id !== state.group ? button("make-home", "Сделать эту группу моей", "star") : ""}</div>`;
  $("#picker").innerHTML =
    `<div class="dialog-head"><h2 id="picker-title">${target === "compare" ? "Группа друга" : "Выберите свою группу"}</h2>${ib("close-picker", "close", "Закрыть")}</div>${quick}<label class="search-field">${icon("search")}<input id="group-search" type="search" placeholder="Название вашей группы" aria-label="Название группы" autocomplete="off"></label><div class="inline-actions" style="margin-top:12px"><select id="department" class="subgroup-select" aria-label="Институт"><option value="">Или выберите институт</option></select><select id="course" class="subgroup-select" aria-label="Курс" hidden><option value="">Курс</option></select></div><div class="picker-results" id="group-results" aria-live="polite"><p class="progress-text">Загружаем каталог…</p></div>`;
  $("#picker").showModal();
  $("#group-search").focus();
  try {
    const r = await data.request("/groups/departments");
    if (g !== pickerGeneration) return;
    $("#department").innerHTML =
      '<option value="">Или выберите институт</option>' +
      (r.departments || [])
        .map((d) => `<option value="${d.id}">${esc(d.name)}</option>`)
        .join("");
    if (config.demo) await searchGroups("");
    else {
      $("#group-results").innerHTML =
        '<p class="progress-text">Введите название группы или выберите институт и курс.</p>';
    }
  } catch {
    $("#group-results").innerHTML =
      '<p class="error-text">Каталог временно недоступен. Попробуйте поиск по названию.</p>';
  }
}
function showGroups(groups) {
  pickerItems = groups;
  $("#group-results").innerHTML = groups.length
    ? groups
        .map(
          (g) =>
            `<button class="pick-option" data-action="choose-group" data-id="${g.id}"><strong>${esc(g.name)}</strong><small>${esc(g.department || "")} ${g.course ? "· " + g.course + " курс" : ""}${g.last_date ? " · Последняя пара " + dateLabel(g.last_date) : ""}${g.shadowed ? " · Копия" : ""}</small></button>`,
        )
        .join("")
    : '<p class="progress-text">Группы не найдены. Попробуйте другое название или курс.</p>';
}
async function searchGroups(q) {
  const g = ++pickerGeneration;
  $("#group-results").innerHTML = '<p class="progress-text">Ищем группу…</p>';
  try {
    const r = await data.request("/groups/search", { q, limit: 50 });
    if (g === pickerGeneration && $("#picker").open) showGroups(r.groups || []);
  } catch {
    if (g === pickerGeneration)
      $("#group-results").innerHTML =
        '<p class="error-text">Не удалось найти группы. Попробуйте ещё раз.</p>';
  }
}
async function chooseGroup(id) {
  const group = pickerItems.find((g) => g.id === id);
  if (!group) return;
  if (pickerTarget === "group") persist();
  state[pickerTarget] = id;
  state[pickerTarget === "group" ? "groupInfo" : "otherInfo"] = group;
  state[pickerTarget === "group" ? "subgroup" : "compareSubgroup"] = state.groupSubgroups[id] || 0;
  if (pickerTarget === "group" && !state.homeGroup) state.homeGroup = { id, name: group.name };
  persist();
  $("#picker").close();
  pickerGeneration++;
  await load();
  toast(
    pickerTarget === "group"
      ? "Группа сохранена на этом устройстве"
      : "Группа друга выбрана",
  );
}
function detail(title, body) {
  detailFocus = focusIdentity(document.activeElement) || detailFocus;
  $("#detail").classList.remove("changes-dialog");
  $("#detail").innerHTML =
    `<div class="dialog-head"><h2 id="detail-title">${title}</h2>${ib("close-detail", "close", "Закрыть")}</div>${body}`;
  $("#detail").showModal();
}
function showLesson(id, date = "") {
  // Дата уточняет, какое именно занятие открыть: в ленте предмета одна и та
  // же пара повторяется каждую неделю, и без даты диалог показал бы первую
  // попавшуюся.
  let l = [
    ...(state.route === "schedule" && (state.subject || state.subjectName)
      ? state.subjectData?.items || []
      : state.route === "teachers" ? items(state.teacherData?.week) : items(state.data?.week)),
  ].find((l) => l.id === id && (!date || l.date === date));
  if (!l) return;
  l = {
    ...l,
    audience_label: audienceName(
      l,
      state.route === "teachers" ? [] : state.subgroups,
      state.route === "teachers" ? (l.groups || []).map(g => g.name).join(", ") : state.groupInfo?.name,
    ),
  };
  detail(
    esc(l.discipline),
    `<div class="dialog-meta"><p>${icon("calendar")}${dateLabel(l.date, { weekday: "long", day: "numeric", month: "long" })}</p><p>${icon("clock")}${l.minute_to > l.minute_from ? clock(l.minute_from) + " – " + clock(l.minute_to) + " МСК" : "Время не указано"}</p><p>${icon("pin")}${esc(placeLabel(l, { online: "Дистанционное занятие" }))}</p>${(l.staff || []).map((s) => `<p>${icon("users")}<button class="text-link" data-action="find-teacher" data-name="${esc(s)}">${esc(s)}</button></p>`).join("")}<p>${icon("book")}${esc([l.class_type, l.audience_label].filter(Boolean).join(" · "))}</p>${state.route === "teachers" && l.groups?.length ? `<p>${icon("users")}${esc(l.groups.map(g => g.name).join(", "))}</p>` : ""}${l.topic ? `<p>${esc(l.topic)}</p>` : ""}${l.comments ? `<p>${esc(l.comments)}</p>` : ""}</div><div class="lesson-actions">${state.route === "schedule" && !state.subject && !state.subjectName && l.discipline ? button("subject", "Про предмет", "book", "primary", `data-name="${esc(l.discipline)}" data-date="${l.date}"`) : ""}${state.route === "schedule" && (state.subject || state.subjectName) ? button("lesson-day", "Расписание дня", "calendar", "primary", `data-date="${l.date}"`) : ""}${button("close-detail", "Закрыть")}</div>`,
  );
}
async function exportWeek() {
  try {
    let response =
      state.data ||
      (await data.request("/schedule/week", {
        group: state.group,
        subgroup: state.subgroup,
        monday: state.week,
      }));
    if (!state.data) {
      const roster = await data.request("/groups/subgroups", {
        group: state.group,
      });
      response = withAudienceNames(response, roster.subgroups || []);
    }
    if (!items(response.week).length) {
      toast("В выбранной неделе нет занятий для экспорта");
      return;
    }
    const blob = new Blob([ics(response.week, response.group.name)], {
        type: "text/calendar;charset=utf-8",
      }),
      url = URL.createObjectURL(blob),
      a = document.createElement("a");
    a.href = url;
    a.download = `raspisanie-${state.week}.ics`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    toast(
      response.missing || response.stale
        ? "Скачана имеющаяся копия; данные могут быть неполными"
        : "Неделя скачана. Файл не обновляется автоматически.",
    );
  } catch {
    toast("Не удалось скачать расписание");
  }
}

document.addEventListener("click", async (event) => {
  const route = event.target.closest("[data-route]");
  if (route) {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)
      return;
    event.preventDefault();
    await navigate(route.dataset.route);
    return;
  }
  const el = event.target.closest("[data-action]");
  if (!el || el.disabled) return;
  const action = el.dataset.action;
  try {
    switch (action) {
      case "pick":
        await openPicker(el.dataset.target);
        break;
      case "quick-group": {
        const group = [state.groupInfo, ...state.favorites].find(g => g?.id === Number(el.dataset.id));
        if (!group) break;
        pickerTarget = el.dataset.target || "group";
        pickerItems = [group];
        await chooseGroup(group.id);
        break;
      }
      case "home-group":
        if (state.homeGroup) {
          persist();
          state.group = state.homeGroup.id;
          state.subgroup = state.groupSubgroups[state.group] || 0;
          persist();
          await navigate("schedule");
        }
        break;
      case "make-home":
        state.homeGroup = { id: state.group, name: state.groupInfo.name };
        persist();
        $("#picker").close();
        render();
        toast("Моя группа сохранена");
        break;
      case "choose-group":
        await chooseGroup(Number(el.dataset.id));
        break;
      case "close-picker":
        $("#picker").close();
        pickerGeneration++;
        break;
      case "close-detail":
        $("#detail").close();
        break;
      case "reload":
        await load();
        break;
      case "revisions-toggle":
        state.revisionMode = !state.revisionMode;
        if (state.revisionMode) await loadRevisions(true);
        else {
          revisionGeneration++;
          state.revisionLoading = false;
          render();
          $('[data-action="revisions-toggle"]')?.focus?.({ preventScroll: true });
        }
        break;
      case "revisions-reload":
        await loadRevisions();
        break;
      case "revision-latest":
        state.revisionID = 0;
        state.revisionFollowLatest = true;
        await loadRevisions();
        break;
      case "revision-step": {
        const index = state.revisions.findIndex(r => r.id === state.revisionID);
        const revision = state.revisions[index + Number(el.dataset.step)];
        if (revision) { state.revisionID = revision.id; state.revisionFollowLatest = revision.id === state.revisions[0]?.id; await loadRevisions(); }
        break;
      }
      case "revision-lesson": {
        const l = revisionRows(state.revision, state.data?.grid).find(l => l.id === Number(el.dataset.id) && l.revisionKind === el.dataset.kind);
        if (l) detail(esc(l.discipline), `<p class="revision-card-mark">${l.revisionKind === "removed" ? "− Было до правки" : l.revisionKind === "added" ? "+ Стало после правки" : "Без изменений"} · снимок #${state.revision.id}</p><p>${dateLabel(l.date)} · ${clock(l.minute_from)} – ${clock(l.minute_to)}</p><p>${esc(placeLabel(l))}</p><p>${esc((l.staff || []).join(", "))}</p><p>${esc(l.audience_label)}</p><p>${esc(l.comments)}</p>${button("close-detail", "Закрыть")}`);
        break;
      }
      case "session-open":
        showSession();
        break;
      case "prev":
      case "next": {
        const step = action === "prev" ? -7 : 7;
        state.week = shift(state.week, step);
        state.date = state.week === monday(today()) ? today() : state.week;
        await load({ soft: true });
        break;
      }
      case "today":
        state.week = monday(today());
        state.date = today();
        await load({ soft: true });
        break;
      case "view":
        if (el.dataset.view === "day" && state.view !== "day" && state.week === monday(today())) state.date = today();
        state.view = el.dataset.view;
        persist();
        syncURL();
        render();
        break;
      case "day":
        state.date = el.dataset.date;
        syncURL();
        render();
        break;
      case "calendar":
        state.date = el.dataset.date;
        state.week = monday(state.date);
        state.view = "day";
        persist();
        await load({ soft: true });
        break;
      case "theme":
        state.theme = nextTheme().value;
        persist();
        applyTheme();
        break;
      case "compare":
        await navigate("compare");
        break;
      case "compare-mode":
        if (!["nearby", "breaks", "free"].includes(el.dataset.mode)) break;
        state.compareMode = el.dataset.mode;
        persist();
        syncURL();
        render();
        break;
      case "teacher":
        state.teacher = Number(el.dataset.id);
        state.teacherSubject = "";
        state.teacherProfileOpen = null;
        history.pushState(null, "", urlFor());
        window.scrollTo({ top: 0 });
        await load();
        break;
      case "all-teachers":
        state.teacher = 0;
        state.teacherSubject = "";
        history.pushState(null, "", urlFor());
        await load();
        break;
      case "teacher-subject":
        state.teacherSubject = el.dataset.name || "";
        syncURL();
        render();
        break;
      case "teacher-scope":
        state.teacherScope = el.dataset.scope === "mine" && state.group ? "mine" : "all";
        await load();
        break;
      case "find-teacher":
        $("#detail").close();
        state.teacher = 0;
        state.teacherQuery = el.dataset.name;
        state.teacherScope = "all";
        state.teacherSubject = "";
        await navigate("teachers");
        break;
      case "lesson":
        showLesson(Number(el.dataset.id), el.dataset.date || "");
        break;
      case "subject":
        $("#detail").close();
        state.subject = Number(el.dataset.id || 0);
        state.subjectName = el.dataset.name || "";
        state.subjectData = null;
        state.subjectDate = validDate(el.dataset.date) ? el.dataset.date : state.date;
        state.subjectFilter = "auto";
        state.subjectLimit = 12;
        state.route = "schedule";
        // Отдельная запись в истории: «назад» возвращает к расписанию, а не
        // уводит с сайта.
        history.pushState(null, "", urlFor("schedule"));
        window.scrollTo({ top: 0 });
        await load();
        break;
      case "schedule-back":
        state.subject = 0;
        state.subjectName = "";
        state.subjectData = null;
        await load();
        break;
      case "lesson-day":
        if (!validDate(el.dataset.date)) break;
        $("#detail").close();
        state.date = el.dataset.date;
        state.week = monday(state.date);
        state.view = "day";
        persist();
        await navigate("schedule");
        break;
      case "subject-more":
        state.subjectLimit += 20;
        render();
        break;
      case "subject-filter":
        if (!["upcoming", "past", "all"].includes(el.dataset.filter)) break;
        state.subjectFilter = el.dataset.filter;
        state.subjectLimit = 12;
        render();
        $(`[data-action="subject-filter"][data-filter="${state.subjectFilter}"]`)?.focus();
        break;
      case "favorite":
        if (state.favorites.some((g) => g.id === state.group))
          state.favorites = state.favorites.filter((g) => g.id !== state.group);
        else {
          if (state.favorites.length >= 12) {
            toast("Можно сохранить до 12 групп");
            break;
          }
          state.favorites.push({ id: state.group, name: state.groupInfo.name });
        }
        persist();
        render();
        break;
      case "favorite-remove":
        state.favorites = state.favorites.filter(
          (g) => g.id !== Number(el.dataset.id),
        );
        persist();
        render();
        break;
      case "favorite-open":
        persist();
        state.group = Number(el.dataset.id);
        state.subgroup = state.groupSubgroups[state.group] || 0;
        persist();
        await navigate("schedule");
        break;
      case "export":
        await exportWeek();
        break;
      case "share": {
        const url = urlFor().toString(),
          result = await share(url, document.title);
        if (result === null)
          detail(
            "Ссылка на расписание",
            `<label class="search-field"><input value="${esc(url)}" readonly aria-label="Ссылка для копирования"></label><p class="small muted" style="margin-top:15px">Скопируйте ссылку и отправьте другу.</p>`,
          );
        else toast(result);
        break;
      }
      case "meeting":
        detail(
          "Время для встречи",
          `<p>${dateLabel(el.dataset.date, { weekday: "long", day: "numeric", month: "long" })}</p><div class="dialog-meta"><p>${icon("clock")}${clock(Number(el.dataset.from))} – ${clock(Number(el.dataset.to))} МСК</p><p>${icon("users")}${esc(state.groupInfo.name)} и ${esc(state.otherInfo.name)}</p></div><p class="small muted">В расписаниях обеих групп на это время нет занятий. Оставьте время на дорогу и договоритесь о месте.</p><div style="margin-top:20px">${button("share-meeting", "Поделиться сравнением", "share", "primary")}</div>`,
        );
        break;
      case "share-meeting":
        toast(
          (await share(
            urlFor("compare").toString(),
            "Найдём время для встречи",
          )) || "Скопируйте ссылку через кнопку «Поделиться» вверху",
        );
        break;
      case "install":
        if (!(await install()))
          detail(
            "На главный экран",
            `<div class="dialog-meta"><p>${icon("phone")}iPhone: откройте сайт в Safari → «Поделиться» → «На экран Домой».</p><p>${icon("phone")}Android: меню браузера → «Установить приложение» или «Добавить на главный экран».</p></div><p class="small muted">В Telegram для установки откройте сайт во внешнем браузере. Установка доступна на защищённом HTTPS-адресе.</p>`,
          );
        else toast("Приложение установлено");
        break;
      case "twins": {
        await openPicker("group");
        const g = ++pickerGeneration;
        const r = await data.request("/groups/twins", { group: state.group });
        if (g === pickerGeneration) {
          if ((r.twins || []).length > 1) showGroups(r.twins);
          else
            $("#group-results").innerHTML =
              '<p class="progress-text">Других активных копий этой группы нет.</p>';
        }
        break;
      }
      case "reset":
        detail(
          "Сбросить память сайта?",
          `<p class="muted">Удалятся настройки и сохранённые расписания в этом браузере. Настройки бота останутся прежними.</p><div class="inline-actions" style="margin-top:22px">${button("confirm-reset", "Сбросить", "", "primary")}${button("close-detail", "Отмена")}</div>`,
        );
        break;
      case "confirm-reset":
        storage.removeItem("mp.changes.v1");
        storage.removeItem("demo.mp.changes.v1");
        storage.removeItem(
          config.demo ? "demo.mp.preferences.v1" : "mp.preferences.v1",
        );
        storage.removeItem("mp.schedule-cache.v1");
        if (!config.demo)
          document.cookie = "mp_group=; Max-Age=0; Path=/; SameSite=Lax";
        location.replace("/");
        break;
    }
  } catch {
    toast("Действие не получилось. Попробуйте ещё раз.");
  }
});
document.addEventListener("input", (event) => {
  if (event.target.id === "group-search") {
    clearTimeout(pickerTimer);
    ++pickerGeneration;
    const q = event.target.value;
    pickerTimer = setTimeout(() => searchGroups(q), 250);
  }
  if (event.target.id === "teacher-search") {
    state.teacherQuery = event.target.value;
    syncURL();
    clearTimeout(searchTimer);
    const g = ++searchGeneration;
    searchTimer = setTimeout(async () => {
      const results = $("#teacher-results");
      if (!results) return;
      results.innerHTML = '<p class="progress-text">Ищем преподавателей…</p>';
      try {
        const r = await data.request("/teachers/search", teacherSearchParams());
        if (
          g === searchGeneration &&
          state.route === "teachers" &&
          !state.teacher
        ) {
          state.teacherCatalog = r;
          state.teacherResults = r.teachers || [];
          $("#teacher-results").innerHTML = teacherCards();
        }
      } catch {
        if (g === searchGeneration && $("#teacher-results"))
          $("#teacher-results").innerHTML =
            '<p class="error-text">Поиск временно недоступен. Попробуйте ещё раз.</p>';
      }
    }, 250);
  }
});
document.addEventListener("toggle", event => {
  if (event.target.id === "revision-details") state.revisionDetailsOpen = event.target.open;
  if (event.target.id === "teacher-profile") state.teacherProfileOpen = event.target.open;
}, true);
document.addEventListener("change", async (event) => {
  const el = event.target;
  try {
    if (el.id === "revision-only") {
      state.revisionOnlyChanges = el.checked;
      render();
    }
    if (el.id === "revision-select") {
      state.revisionID = Number(el.value);
      state.revisionFollowLatest = state.revisionID === state.revisions[0]?.id;
      await loadRevisions();
    }
    if (el.id === "subgroup" || el.id === "picker-subgroup") {
      if (el.id === "picker-subgroup") $("#picker").close();
      state.subgroup = Number(el.value);
      persist();
      await load();
    }
    if (el.id === "compare-subgroup") {
      state.compareSubgroup = Number(el.value);
      persist();
      await load();
    }
    if (el.id === "meeting-min") {
      state.minMeeting = Number(el.value);
      persist();
      syncURL();
      render();
    }
    if (el.id === "meeting-end") {
      state.meetingEnd = Number(el.value);
      persist();
      syncURL();
      render();
    }
    if (el.id === "proximity-filter") {
      state.proximity = el.value;
      persist();
      syncURL();
      render();
    }
    if (el.id === "default-view") {
      state.view = el.value;
      persist();
    }
    if (el.id === "show-transfers") {
      state.showTransfers = el.value === "on";
      persist();
    }
    if (el.id === "theme-select") {
      state.theme = el.value;
      persist();
      applyTheme();
    }
    if (el.id === "department") {
      const g = ++pickerGeneration;
      $("#course").hidden = true;
      $("#group-results").innerHTML = "";
      if (!el.value) return;
      const r = await data.request("/groups/list", { department: el.value });
      if (g !== pickerGeneration) return;
      $("#course").innerHTML =
        '<option value="">Выберите курс</option>' +
        (r.courses || [])
          .map((c) => `<option value="${c}">${c} курс</option>`)
          .join("");
      $("#course").hidden = false;
    }
    if (el.id === "course") {
      const g = ++pickerGeneration;
      if (!el.value) return;
      $("#group-results").innerHTML =
        '<p class="progress-text">Загружаем группы…</p>';
      const r = await data.request("/groups/list", {
        department: $("#department").value,
        course: el.value,
      });
      if (g === pickerGeneration) showGroups(r.groups || []);
    }
  } catch {
    toast("Не удалось загрузить данные. Попробуйте ещё раз.");
  }
});
$("#detail").addEventListener("close", () => {
  restoreFocus(detailFocus);
  detailFocus = null;
});
$("#picker").addEventListener("close", () => {
  ++pickerGeneration;
  clearTimeout(pickerTimer);
});
window.addEventListener("online", () => {
  toast("Подключение восстановлено");
  refresh(0);
});
window.addEventListener("offline", () =>
  toast("Нет подключения. Доступны ранее открытые расписания."),
);
// Возврат на вкладку и раз в пять минут — тихо, без опустошения страницы.
function updateClock() {
  minuteCache.at = 0;
  if (document.visibilityState !== "visible" || state.loading || state.error || !state.data || state.revisionMode ||
      state.route !== "schedule" || state.subject || state.subjectName || $("#picker").open || $("#detail").open) return;
  const focus = $(".focus-card");
  if (focus && document.activeElement !== focus) focus.outerHTML = nextCard();
  const summary = $(".my-day");
  if (summary && !summary.contains?.(document.activeElement) && state.week === monday(today())) {
    const expanded = summary.querySelector?.("details")?.open;
    const html = myDayView();
    summary.outerHTML = expanded ? html.replace('<details class="my-day-details">', '<details class="my-day-details" open>') : html;
  }
  if (!document.querySelectorAll) return;
  const lessons = items(state.data.week);
  for (const card of document.querySelectorAll('.schedule-area .lesson[data-action="lesson"]')) {
    const l = lessons.find(l => l.id === Number(card.dataset.id));
    card.classList.toggle("now", !!l && l.date === today() && l.minute_from <= nowMinute() && nowMinute() < l.minute_to);
  }
  for (const line of document.querySelectorAll(".schedule-area .now-line")) line.remove();
  const day = state.data.week.days.find(d => d.date === today());
  if (!day || (state.view === "day" && state.date !== today())) return;
  const rows = groupedDayRows(day, state.data.grid), mark = nowIndex(rows, day.date);
  if (mark < 0) return;
  const container = state.view === "day" ? $(".day-list") : $(`.day-column[data-date="${day.date}"] .day-contents`);
  const row = container?.querySelectorAll?.(":scope > .schedule-row")[mark];
  row?.insertAdjacentHTML("afterbegin", nowLine());
}
setInterval(updateClock, 60000);
document.addEventListener("visibilitychange", () => { updateClock(); refresh(); });
setInterval(() => refresh(), 300000);
if (window.mpBoot) window.mpBoot.stage = "platform";
await initPlatform(
  () => {
    state.subject = 0;
    state.subjectName = "";
    state.subjectData = null;
    navigate("schedule");
  },
  (theme) => {
    telegramTheme = theme;
    applyTheme();
  },
);
state.route = routeFromURL();
if (window.mpBoot) window.mpBoot.stage = "schedule";
await load();
if (window.mpBoot) window.mpBoot.stage = "ready";
if (document.modelContext?.registerTool) {
  const lifecycle = new AbortController();
  window.addEventListener("pagehide", () => lifecycle.abort(), { once: true });
  try {
    await document.modelContext.registerTool(
      {
        name: "read_schedule_week",
        description:
          "Read the currently loaded schedule week, group, subgroup and data freshness.",
        inputSchema: {
          type: "object",
          properties: {},
          additionalProperties: false,
        },
        annotations: { readOnlyHint: true, untrustedContentHint: true },
        execute(input) {
          if (!input || typeof input !== "object" || Object.keys(input).length)
            throw new Error("Expected an empty object");
          return {
            group: state.teacherData ? null : state.groupInfo,
            teacher: state.teacherData?.teacher || null,
            subgroup: state.teacherData ? 0 : state.subgroup,
            week: state.teacherData?.week || state.data?.week || null,
            missing: state.teacherData?.missing ?? state.data?.missing ?? true,
            stale: state.teacherData?.stale ?? state.data?.stale ?? true,
            offline: state.teacherData?.offline ?? state.data?.offline ?? false,
            demo: config.demo,
          };
        },
      },
      { signal: lifecycle.signal },
    );
  } catch {
    /* Optional browser proposal; regular UI remains available. */
  }
}
