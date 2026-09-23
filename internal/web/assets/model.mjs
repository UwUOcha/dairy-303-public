import { installation } from "./config.mjs";
export const TZ = installation.timezone || "UTC";
export const esc = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export const clock = (n, format = "24") => {
  const hour = Math.floor(n / 60), minute = String(n % 60).padStart(2, "0");
  return format === "12"
    ? `${hour % 12 || 12}:${minute} ${hour % 24 < 12 ? "AM" : "PM"}`
    : `${String(hour).padStart(2, "0")}:${minute}`;
};
export function today() {
  return new Intl.DateTimeFormat("sv-SE", { timeZone: TZ }).format(new Date());
}
export function minutesNow() {
  const p = new Intl.DateTimeFormat("en-GB", {
    timeZone: TZ,
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  })
    .format(new Date())
    .split(":");
  return Number(p[0]) * 60 + Number(p[1]);
}
export function shift(date, n) {
  const d = new Date(`${date}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}
export function monday(date) {
  return shift(date, -((new Date(`${date}T12:00:00Z`).getUTCDay() + 6) % 7));
}
export function dateLabel(date, options = { day: "numeric", month: "long" }) {
  return new Intl.DateTimeFormat("ru-RU", { ...options, timeZone: TZ }).format(
    new Date(`${date}T12:00:00Z`),
  );
}
export const actual = (l) => !((l.flags || 0) & 9);
export const items = (week) =>
  (week?.days || []).flatMap((d) => d.items || []).filter(actual);

export function audienceName(lesson, subgroups = [], groupName = "") {
  if (!lesson.subgroup_id) return lesson.audience_label || groupName;
  const name = subgroups.find((s) => s.id === lesson.subgroup_id)?.name?.trim();
  if (name)
    return /^\d+$/.test(name) && groupName ? `${groupName}/${name}` : name;
  // A global subgroup ID (e.g. 334) is not its ordinal number. Do not invent
  // a suffix, or silently label a subgroup class as one for the whole group.
  const label = lesson.audience_label?.trim() || "";
  return /\/[^/\s]+$/.test(label) || /подгрупп/i.test(label)
    ? label
    : "Подгруппа";
}
export function withAudienceNames(response, subgroups = []) {
  return {
    ...response,
    week: {
      ...response.week,
      days: response.week.days.map((d) => ({
        ...d,
        items: (d.items || []).map((l) => ({
          ...l,
          audience_label: audienceName(l, subgroups, response.group?.name),
        })),
      })),
    },
  };
}

export function scheduleFocus(week, date = today(), minute = minutesNow()) {
  const lessons = [...items(week)].sort(
    (a, b) => a.date.localeCompare(b.date) || a.minute_from - b.minute_from,
  );
  const currentWeek = monday(date);
  if (week.monday < currentWeek)
    return { kind: "past", title: "Прошедшая неделя", lesson: null };
  if (week.monday > currentWeek)
    return {
      kind: "future",
      title: "Первая пара выбранной недели",
      lesson: lessons[0] || null,
    };
  const current = lessons.find(
    (l) => l.date === date && l.minute_from <= minute && l.minute_to > minute,
  );
  if (current)
    return {
      kind: "current",
      title: "Сейчас по расписанию",
      lesson: current,
      remaining: current.minute_to - minute,
    };
  const next = lessons.find(
    (l) =>
      (l.date > date || (l.date === date && l.minute_from > minute)) &&
      l.minute_to > l.minute_from,
  );
  if (next)
    return {
      kind: "next",
      title: "Следующая пара",
      lesson: next,
      wait: next.date === date ? next.minute_from - minute : null,
    };
  const unknown = lessons.find(
    (l) => l.date >= date && l.minute_to <= l.minute_from,
  );
  if (unknown)
    return { kind: "unknown", title: "Время пары не указано", lesson: unknown };
  return { kind: "done", title: "Пары на этой неделе", lesson: null };
}
export function intervals(lessons, from = 0, to = 1440) {
  const sorted = lessons
    .filter(actual)
    .filter((l) => l.minute_to > l.minute_from)
    .map((l) => [Math.max(from, l.minute_from), Math.min(to, l.minute_to)])
    .filter(([a, b]) => b > a)
    .sort((a, b) => a[0] - b[0]);
  const merged = [];
  for (const [a, b] of sorted) {
    const last = merged.at(-1);
    if (last && a <= last[1]) last[1] = Math.max(last[1], b);
    else merged.push([a, b]);
  }
  return merged;
}
export function freeTogether(a, b, { from = 510, to = 1080, min = 30 } = {}) {
  // An unknown bell time could hide a conflict. Do not promise a free slot.
  if ([...a, ...b].some((l) => actual(l) && l.minute_to <= l.minute_from))
    return [];
  const busy = intervals([...a, ...b], from, to),
    out = [];
  let cursor = from;
  for (const [start, end] of busy) {
    if (start - cursor >= min) out.push([cursor, start]);
    cursor = Math.max(cursor, end);
  }
  if (to - cursor >= min) out.push([cursor, to]);
  return out;
}
export function statistics(week) {
  const all = items(week),
    subjects = new Map(),
    types = new Map();
  let minutes = 0,
    gapMinutes = 0,
    studyDays = 0;
  const days = (week?.days || []).map((d) => {
    const ls = (d.items || []).filter(actual),
      busy = intervals(ls);
    const duration = busy.reduce((n, [a, b]) => n + b - a, 0);
    minutes += duration;
    if (ls.length) studyDays++;
    for (let i = 1; i < busy.length; i++) {
      const gap = busy[i][0] - busy[i - 1][1];
      if (gap >= 40) gapMinutes += gap;
    }
    return { date: d.date, count: ls.length, minutes: duration };
  });
  for (const l of all) {
    subjects.set(l.discipline, (subjects.get(l.discipline) || 0) + 1);
    const type = l.class_type || "Другое";
    types.set(type, (types.get(type) || 0) + 1);
  }
  return {
    count: all.length,
    minutes,
    gapMinutes,
    studyDays,
    days,
    subjects: [...subjects].sort((a, b) => b[1] - a[1]),
    types: [...types].sort((a, b) => b[1] - a[1]),
  };
}
export function ics(week, label, stamp = new Date(), timezone = TZ) {
  const escape = (s) =>
    String(s || "")
      .replace(/\\/g, "\\\\")
      .replace(/\r?\n/g, "\\n")
      .replace(/,/g, "\\,")
      .replace(/;/g, "\\;");
  // Смещение берём на дату занятия: оно может меняться в течение года.
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  });
  const utc = (date, min) => {
    const wall = new Date(`${date}T00:00:00Z`).getTime() + min * 60000;
    let instant = wall;
    for (let i = 0; i < 4; i++) {
      const p = Object.fromEntries(parts.formatToParts(new Date(instant)).map(p => [p.type, p.value]));
      const local = Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second);
      const correction = wall - local;
      if (!correction) return instant;
      instant += correction;
    }
    throw new Error("Время занятия не существует в часовом поясе вуза");
  };
  const fmt = (n) =>
    new Date(n)
      .toISOString()
      .replace(/[-:]/g, "")
      .replace(/\.\d{3}/, "");
  const lines = [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//Mezhdu Parami//Schedule//RU",
    "CALSCALE:GREGORIAN",
    `X-WR-CALNAME:${escape(label)}`,
  ];
  for (const l of items(week).filter((l) => l.minute_to > l.minute_from))
    lines.push(
      "BEGIN:VEVENT",
      `UID:lesson-${l.id}-${l.date}@mezhdu-parami`,
      `DTSTAMP:${fmt(stamp)}`,
      `DTSTART:${fmt(utc(l.date, l.minute_from))}`,
      `DTEND:${fmt(utc(l.date, l.minute_to))}`,
      `SUMMARY:${escape(l.discipline)}`,
      `LOCATION:${escape(l.classroom)}`,
      `DESCRIPTION:${escape([l.class_type, (l.staff || []).join(", "), l.audience_label, l.comments].filter(Boolean).join("\n"))}`,
      "END:VEVENT",
    );
  lines.push("END:VCALENDAR");
  // RFC 5545 lines are folded at 75 octets, not 75 Cyrillic characters.
  return (
    lines
      .map((line) => {
        let out = "",
          length = 0;
        for (const c of line) {
          const bytes = new TextEncoder().encode(c).length;
          if (length + bytes > 75) {
            out += "\r\n ";
            length = 1;
          }
          out += c;
          length += bytes;
        }
        return out;
      })
      .join("\r\n") + "\r\n"
  );
}
export function readPreferences(storage, cookie = "") {
  let data = {};
  try {
    data = JSON.parse(storage.getItem("mp.preferences.v1") || "{}") || {};
  } catch {}
  const id = Number(cookie.match(/(?:^|;\s*)mp_group=(\d+)/)?.[1]);
  return {
    group:
      Number.isSafeInteger(data.group) && data.group > 0 ? data.group : id || 0,
    subgroup:
      Number.isSafeInteger(data.subgroup) && data.subgroup > 0
        ? data.subgroup
        : 0,
    theme: ["light", "dark", "night", "system"].includes(data.theme)
      ? data.theme
      : "system",
    view: data.view === "day" ? "day" : "week",
    showTransfers: data.showTransfers === true,
    favorites: Array.isArray(data.favorites)
      ? data.favorites
          .filter(
            (g) =>
              Number.isSafeInteger(g?.id) &&
              g.id > 0 &&
              typeof g?.name === "string",
          )
          .slice(0, 12)
      : [],
    compare:
      Number.isSafeInteger(data.compare) && data.compare > 0 ? data.compare : 0,
    compareSubgroup:
      Number.isSafeInteger(data.compareSubgroup) && data.compareSubgroup > 0
        ? data.compareSubgroup
        : 0,
    compareMode: ["nearby", "breaks", "free"].includes(data.compareMode)
      ? data.compareMode
      : "nearby",
    proximity: ["all", "floor", "room"].includes(data.proximity)
      ? data.proximity
      : "all",
  };
}

// dayRows раскладывает день по сетке звонков: пары вперемешку с окнами.
//
// Окно здесь — не только промежуток между парами, но и пустые слоты до первой
// пары. Без них расписание, начинающееся в 10:25, выглядит так же, как
// расписание, начинающееся в 8:30, и человек приходит на полтора часа раньше.
// Пары, заведённые вузом как пустые (FlagEmpty), — те же окна, а не карточки.
// Хвост дня не рисуем: после последней пары человек свободен и так.
export function dayRows(day, grid) {
  const items = (day?.items || []).filter((l) => !((l.flags || 0) & 1));
  const times = [...(grid?.times || [])]
    .filter((t) => t.number > 0)
    .sort((a, b) => a.minute_from - b.minute_from);
  const numbered = items.filter((l) => l.number > 0 && actual(l));
  // Без сетки или без нумерации слотов угадывать пустые пары нечем: отдаём
  // день как есть, окна между парами покажет gap_before с сервера.
  if (!times.length || !numbered.length)
    return items.map((lesson) => ({
      lesson,
      gapBefore: lesson.gap_before || 0,
    }));
  const last = Math.max(...numbered.map((l) => l.number));
  const busy = new Set(items.map((l) => l.number));
  const rows = [];
  let run = [];
  const flush = () => {
    if (!run.length) return;
    rows.push({
      free: {
        from: run[0].minute_from,
        to: run[run.length - 1].minute_to,
        slots: run.map((t) => t.number),
        lead: !rows.length,
      },
    });
    run = [];
  };
  for (const t of times) {
    if (t.number > last) break;
    if (!busy.has(t.number)) {
      run.push(t);
      continue;
    }
    flush();
    for (const lesson of items.filter((l) => l.number === t.number))
      rows.push({ lesson, gapBefore: 0 });
  }
  // Пары вне сетки (неизвестный time_id) всё равно надо показать.
  for (const lesson of items.filter(
    (l) => !times.some((t) => t.number === l.number),
  ))
    rows.push({ lesson, gapBefore: 0 });
  return rows;
}

// nowRow — перед какой строкой дня стоит человек в указанную минуту.
//
// Колонка дня — стопка карточек, а не пропорциональная шкала времени, поэтому
// отметка «сейчас» встаёт границей: перед первой строкой, которая ещё не
// закончилась. День уже кончился — отметки нет, она бы врала. Пары с неизвестным
// временем пропускаем: ставить отметку по выдуманной границе нельзя.
export function nowRow(rows, minute) {
  return rows.findIndex((row) =>
    row.free
      ? row.free.to > minute
      : row.lesson.minute_to > row.lesson.minute_from &&
        row.lesson.minute_to > minute,
  );
}

// placeLabel — где идёт занятие.
//
// Вуз различает две вещи, которые сайт раньше сливал в одну строку. Пустая
// аудитория значит «мы не знаем»; «без ауд.» вуз пишет там, где аудитории нет
// по существу — выезд, практика на базе, физкультура. Есть и явные места вроде
// «Базы практик (МИ)»: их надо показывать как есть, а не прятать под заглушку.
export function placeLabel(
  lesson,
  { missing = "Аудитория не указана", online = "Онлайн" } = {},
) {
  if ((lesson.flags || 0) & 4) return online;
  const room = String(lesson.classroom || "").trim();
  if (!room) return missing;
  if (/^без\s*ауд/iu.test(room)) return "Без аудитории";
  return room;
}

// sessionKind — что это за испытание. Повторяет разбор сервера: карточку
// экзамена надо отличать в сетке недели, а не только на экране сессии.
export function sessionKind(classType) {
  const canonical = { exam: "Экзамен", credit: "Зачёт", graded_credit: "Дифзачёт", consultation: "Консультация", coursework: "Курсовая" };
  if (canonical[classType]) return canonical[classType];
  const t = String(classType || "")
    .trim()
    .toLocaleLowerCase("ru");
  if (t.startsWith("экз")) return "Экзамен";
  if (t.startsWith("диф")) return "Дифзачёт";
  if (t.startsWith("зач")) return "Зачёт";
  if (t.startsWith("конс")) return "Консультация";
  if (t.startsWith("кп") || t.startsWith("курсов")) return "Курсовая";
  return "";
}

// daysUntil — сколько ночей до даты. Считаем по календарным дням, а не по
// часам: «через 2 дня» человек понимает как «послезавтра», сколько бы часов
// до экзамена ни оставалось.
export function daysUntil(date, from = today()) {
  return Math.round(
    (Date.parse(`${date}T12:00:00Z`) - Date.parse(`${from}T12:00:00Z`)) /
      86400000,
  );
}

// plural — русская форма числительного. «Через 21 день» и «через 11 дней»
// правилом «меньше пяти» не выводятся, а ошибка в такой фразе бросается в
// глаза сильнее, чем кажется.
export function plural(n, one, few, many) {
  const abs = Math.abs(n) % 100,
    last = abs % 10;
  if (abs > 10 && abs < 20) return many;
  if (last === 1) return one;
  if (last >= 2 && last <= 4) return few;
  return many;
}

// semesterOf — границы учебного полугодия, повторяют разбор сервера
// (schedule.Semester). Ответ приносит from и to сам, но демонстрация собирает
// предмет без сервера, а подпись «полугодие с … по …» нужна раньше ответа.
export function semesterOf(date = today()) {
  const year = Number(date.slice(0, 4));
  const starts = (installation.term_starts || ["02-01", "09-01"]).flatMap(md => [year - 1, year, year + 1].map(y => `${y}-${md}`)).sort();
  for (let i = 0; i + 1 < starts.length; i++) if (starts[i] <= date && date < starts[i + 1]) return [starts[i], shift(starts[i + 1], -1)];
}

// lessonKind — вид занятия: ключ для оформления и подпись для человека.
// Разбор один на карточку пары, ленту предмета и соотношение видов: развести
// их значило бы однажды назвать одну и ту же пару лекцией в сетке и практикой
// в сводке.
export function lessonKind(classType) {
  const session = sessionKind(classType);
  if (session) return { key: "session", label: session };
  const type = String(classType || "").toLocaleLowerCase("ru");
  if (type.startsWith("лек")) return { key: "lecture", label: "Лекция" };
  if (type.startsWith("пр")) return { key: "practice", label: "Практика" };
  if (type.startsWith("л"))
    return {
      key: "lab",
      label: type === "лб" ? "Лабораторная" : classType || "Занятие",
    };
  return { key: "", label: classType || "Занятие" };
}

// One ordered list drives the summary, controls and lesson feed.
export function subjectSummary(lessons, now = today(), minute = -1) {
  const all = (lessons || []).filter(actual).sort(
    (a, b) => a.date.localeCompare(b.date) || a.minute_from - b.minute_from || a.id - b.id,
  );
  const kinds = new Map(), teachers = new Map(), rooms = new Map(), days = new Map();
  const past = [], upcoming = [];
  let unknownTime = 0;
  for (const l of all) {
    const timed = l.minute_to > l.minute_from;
    (l.date < now || (l.date === now && timed && minute >= l.minute_to) ? past : upcoming).push(l);
    if (!timed) unknownTime++;
    if (!days.has(l.date)) days.set(l.date, []);
    days.get(l.date).push(l);
    const kind = lessonKind(l.class_type);
    const seen = kinds.get(kind.label) || { ...kind, count: 0 };
    seen.count++;
    kinds.set(kind.label, seen);
    for (const name of new Set(l.staff || [])) {
      const t = teachers.get(name) || { name, count: 0, kinds: [] };
      t.count++;
      if (!t.kinds.includes(kind.label)) t.kinds.push(kind.label);
      teachers.set(name, t);
    }
    const place = placeLabel(l, { missing: "" });
    if (place) rooms.set(place, (rooms.get(place) || 0) + 1);
  }
  // Parallel subgroup lessons occupy the same time, even when counted separately.
  const minutes = [...days.values()].reduce(
    (total, ls) => total + intervals(ls).reduce((n, [a, b]) => n + b - a, 0), 0,
  );
  const current = upcoming.find(l => l.date === now && l.minute_to > l.minute_from && l.minute_from <= minute && minute < l.minute_to);
  return {
    all, past, upcoming, current,
    total: all.length, done: past.length, left: upcoming.length,
    minutes, unknownTime, next: current || upcoming[0] || null,
    assessments: upcoming.filter(l => sessionKind(l.class_type)),
    kinds: [...kinds.values()].sort((a, b) => b.count - a.count),
    teachers: [...teachers.values()].sort((a, b) => b.count - a.count),
    rooms: [...rooms].sort((a, b) => b[1] - a[1]),
  };
}

// Teacher weeks contain unique lessons, including shared lectures. Filter before
// counting; unknown times cannot establish a current or next appointment.
export function teacherWeekSummary(week, subject = "", date = today(), minute = minutesNow()) {
  const lessons = items(week).filter(l => !subject || l.discipline === subject)
    .sort((a, b) => a.date.localeCompare(b.date) || a.minute_from - b.minute_from || a.id - b.id);
  const groups = new Map(), rooms = new Map();
  for (const l of lessons) {
    for (const g of l.groups || []) groups.set(g.id, g);
    const place = placeLabel(l);
    if (l.classroom || (l.flags & 4)) rooms.set(place, (rooms.get(place) || 0) + 1);
  }
  const next = lessons.find(l => l.minute_to > l.minute_from &&
    (l.date > date || (l.date === date && l.minute_to > minute))) || null;
  return { lessons, groups: [...groups.values()], days: new Set(lessons.map(l => l.date)).size,
    rooms: [...rooms].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name, "ru")),
    next, current: !!next && next.date === date && next.minute_from <= minute };
}
