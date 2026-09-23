// UI-specific normalization is separate from the cached schedule model. All
// dependencies below are longstanding exports, so an older PWA cache can load
// this new module safely while the service worker updates its app shell.
import { dayRows, actual, intervals, lessonKind, clock, dateLabel, placeLabel, readPreferences as basePreferences } from "./model.mjs";
import { snapshot, diffLessons } from "./changes.mjs";

// Preserve existing initials; only abbreviate full given names.
export function shortName(value) {
  const name = String(value || "").trim().replace(/\s+/gu, " ");
  const parts = name.split(" ");
  if (parts.length < 2) return name;
  const rest = parts.slice(1).join(" ");
  if (/^(?:\p{L}\.\s*)+$/u.test(rest))
    return parts[0] + " " + rest.replace(/\s+/g, "");
  if (parts.length < 3 || !parts.slice(1).every(p => /^[\p{L}-]+$/u.test(p))) return name;
  return parts[0] + " " + parts.slice(1).map(p => p.split("-").map(n => n[0] + ".").join("-")).join("");
}

// Same-time subgroup lessons occupy one row, never consecutive time slots.
export function groupedDayRows(day, grid) {
  const rows = dayRows(day, grid), out = [];
  for (const row of rows) {
    const l = row.lesson;
    const previous = out[out.length - 1];
    if (l && actual(l) && l.minute_to > l.minute_from && previous?.lesson && actual(previous.lesson) &&
        previous.lesson.minute_from === l.minute_from && previous.lesson.minute_to === l.minute_to) {
      previous.lessons.push(l);
    } else out.push(l ? { ...row, lessons: [l] } : row);
  }
  return out;
}
// Multiple lesson pairs can describe one place/time opportunity. Preserve all
// evidence, but don't count mirrored transitions as separate opportunities.
export function groupedNearby(matches) {
  const groups = new Map();
  for (const m of matches) {
    const key = `${m.a.date}:${m.from}:${m.to}:${m.building}:${m.sharedLesson ? "shared:" + m.a.id : m.timing}`;
    if (!groups.has(key)) groups.set(key, { ...m, alternatives: [] });
    const group = groups.get(key);
    group.alternatives.push(m);
    if (group.kind !== m.kind || group.floor !== m.floor || (group.kind === "room" && group.a.classroom !== m.a.classroom)) {
      group.kind = "building";
      group.floor = null;
    }
  }
  return [...groups.values()];
}
// Describe consequences from the same normalized versions used for the cards.
export function revisionSummary(revision) {
  const normalize = rows => snapshot({ days: [{ items: rows || [] }] }).map(({ topic, ...l }) => l);
  const before = normalize(revision.before), after = normalize(revision.after);
  return diffLessons(before, after).map(c => {
    const l = c.after || c.before;
    const when = `${dateLabel(l.date, { weekday: "short", day: "numeric", month: "short" })} · ${clock(l.minute_from)}`;
    const name = `${when} · ${l.discipline}`;
    if (c.kind === "added") return `${name}: добавлено занятие.`;
    if (c.kind === "removed") {
      const oldDay = before.filter(x => x.date === l.date && x.minute_to > x.minute_from);
      const newDay = after.filter(x => x.date === l.date && x.minute_to > x.minute_from);
      const wasFirst = oldDay.length && l.minute_from === Math.min(...oldDay.map(x => x.minute_from));
      const newStart = newDay.length ? Math.min(...newDay.map(x => x.minute_from)) : null;
      return `${name}: отменено.${wasFirst && newStart !== null && newStart > l.minute_from ? ` Начало дня теперь в ${clock(newStart)}.` : ""}`;
    }
    const updates = [];
    if (c.fields.some(f => ["date", "minute_from", "minute_to"].includes(f)))
      updates.push(`${dateLabel(c.before.date)} ${clock(c.before.minute_from)}–${clock(c.before.minute_to)} → ${dateLabel(c.after.date)} ${clock(c.after.minute_from)}–${clock(c.after.minute_to)}`);
    if ((c.fields.includes("classroom") || c.fields.includes("flags")) && placeLabel(c.before) !== placeLabel(c.after))
      updates.push(`${placeLabel(c.before)} → ${placeLabel(c.after)}`);
    if (c.fields.includes("flags") && ((c.before.flags ^ c.after.flags) & 2))
      updates.push(c.after.flags & 2 ? "добавлена самоподготовка" : "снята отметка самоподготовки");
    if (c.fields.includes("staff")) updates.push(`преподаватель: ${c.before.staff.join(", ") || "не указан"} → ${c.after.staff.join(", ") || "не указан"}`);
    if (c.fields.includes("comments")) updates.push("изменилось примечание");
    if (c.fields.includes("subgroup_id") || c.fields.includes("audience_label")) updates.push("изменился состав группы");
    if (c.fields.includes("discipline")) updates.push(`предмет: ${c.before.discipline} → ${c.after.discipline}`);
    if (c.fields.includes("class_type")) updates.push(`вид занятия: ${c.before.class_type} → ${c.after.class_type}`);
    return `${name}: ${updates.join("; ") || "изменились сведения о занятии"}.`;
  });
}

export function readPreferences(storage, cookie = "") {
  const base = basePreferences(storage, cookie);
  let data = {};
  try { data = JSON.parse(storage.getItem("mp.preferences.v1") || "{}") || {}; } catch {}
  return {
    ...base,
    timeFormat: data.timeFormat === "12" ? "12" : "24",
    groupSubgroups: Object.fromEntries(Object.entries(data.groupSubgroups && typeof data.groupSubgroups === "object" ? data.groupSubgroups : {})
      .filter(([g, s]) => /^\d+$/.test(g) && Number.isSafeInteger(Number(g)) && Number(g) > 0 && Number.isSafeInteger(s) && s >= 0).slice(-24)),
    homeGroup: Number.isSafeInteger(data.homeGroup?.id) && data.homeGroup.id > 0
      ? { id: data.homeGroup.id, name: String(data.homeGroup.name || "") }
      : Number.isSafeInteger(data.group) && data.group > 0 ? { id: data.group, name: "" } : null,
    minMeeting: [15, 30, 60, 90].includes(data.minMeeting) ? data.minMeeting : 30,
    meetingEnd: [1080, 1200, 1320].includes(data.meetingEnd) ? data.meetingEnd : 1080,
  };
}

// Unknown times keep today's lessons visible: an absent end is not a finished day.
export function studyDay(days, date, minute) {
  return [...(days || [])].sort((a, b) => a.date.localeCompare(b.date)).find(d =>
    d.date >= date && (d.items || []).some(l => actual(l) &&
      (d.date > date || !(l.minute_to > l.minute_from) || l.minute_to > minute))) || null;
}

// День, который расписание открывает само: сегодня, пока в нём остались пары,
// иначе ближайший следующий учебный день. Без данных остаётся сегодня.
export function openingDay(days, date, minute) {
  return studyDay(days, date, minute)?.date || date;
}

export function dayOverview(day, grid) {
  const lessons = (day?.items || []).filter(actual).sort((a, b) => a.minute_from - b.minute_from || a.id - b.id);
  const timed = lessons.filter(l => l.minute_to > l.minute_from);
  const unknown = timed.length !== lessons.length;
  const kinds = new Map();
  for (const lesson of lessons) {
    const kind = lessonKind(lesson.class_type);
    const type = String(lesson.class_type || "").trim().toLocaleLowerCase("ru");
    // Keep seminars distinct from practical classes, including upstream abbreviations.
    const label = /^сем(?:\.|инар|$)/u.test(type) ? "Семинары" :
      kind.key === "lecture" ? "Лекции" : kind.key === "practice" ? "Практические" :
      kind.key === "lab" ? "Лабораторные" : !type ? "Тип не указан" : kind.label;
    const entry = kinds.get(label) || { label, key: kind.key, count: 0 };
    entry.count++;
    kinds.set(label, entry);
  }
  const occupied = intervals(timed), windows = [];
  for (let i = 1; i < occupied.length; i++) {
    const from = occupied[i-1][1], to = occupied[i][0];
    // A window contains an entire missed bell slot, not an ordinary break.
    if ((grid?.times || []).some(t => t.minute_from >= from && t.minute_to <= to && t.minute_to > t.minute_from) ||
        timed.some(l => l.minute_from === to && l.gap_before > 0)) windows.push({ from, to });

  }
  return { lessons, count: lessons.length, kinds: [...kinds.values()], unknown,
    start: unknown || !timed.length ? null : timed[0].minute_from,
    end: unknown || !timed.length ? null : Math.max(...timed.map(l => l.minute_to)),
    first: unknown ? [] : timed.filter(l => l.minute_from === timed[0]?.minute_from),
    windows: unknown ? [] : windows };
}
