import { shift, clock, monday, semesterOf, items } from "./model.mjs";
export const groups = [
  { id: 39, name: "ГР-22", department: "Учебный факультет", course: 2 },
  { id: 40, name: "ГР-23", department: "Учебный факультет", course: 2 },
  {
    id: 262,
    name: "Б-Арх-21",
    department: "Инженерно-технологический институт",
    course: 2,
  },
  {
    id: 415,
    name: "Б-ПИ-31",
    department: "Инженерно-технологический институт",
    course: 3,
  },
];
export const teachers = [
  { id: 1, name: "Соколова Елена Андреевна", full_name: "Соколова Елена Андреевна", degree: "к.м.н.", departments: ["Кафедра нормальной физиологии", "Кафедра медицинской биологии"] },
  { id: 2, name: "Морозов Дмитрий Сергеевич" },
  { id: 3, name: "Волкова Анна Игоревна" },
  { id: 4, name: "Кузнецов Павел Олегович" },
];
const subjects = [
  "Нормальная физиология",
  "Анатомия человека",
  "Гистология, эмбриология",
  "Иностранный язык",
  "Биохимия",
  "Физическая культура",
];
const slots = [
  [510, 605],
  [625, 720],
  [740, 835],
  [865, 960],
  [980, 1075],
];
// Сетка звонков — такая же, как отдаёт настоящий API: без неё демонстрация
// не показала бы пустые пары до первой.
export const demoGrid = {
  times: slots.map(([from, to], i) => ({
    id: i + 1,
    number: i + 1,
    minute_from: from,
    minute_to: to,
    label: [from, to].map(clock).join(" – "),
  })),
  workdays: { 1: true, 2: true, 3: true, 4: true, 5: true, 6: true },
};
// Explicitly fictional fixtures. Never used as a fallback for a failed API.
export function demoWeek(group, start, subgroup = 0, teacher = 0) {
  const patterns =
    group === 40
      ? [[1, 2, 3], [0, 1, 2], [1, 3], [0, 1, 2, 3], [1, 2], []]
      : [[0, 1, 3], [1, 2, 3], [0, 1, 2, 3], [0, 2], [0, 1, 2], []];
  return {
    monday: start,
    days: patterns.map((p, day) => ({
      date: shift(start, day),
      weekday: day + 1,
      workday: true,
      items: p
        .map((slot, i) => {
          const si = (day + i) % subjects.length,
            t = teachers[si % teachers.length];
          return {
            id: group * 100 + day * 10 + i,
            date: shift(start, day),
            number: slot + 1,
            time_id: slot + 1,
            minute_from: slots[slot][0],
            minute_to: slots[slot][1],
            time_label: slots[slot].map(clock).join(" – "),
            discipline:
              group === 262
                ? [
                    "Архитектурное проектирование",
                    "История архитектуры",
                    "Рисунок",
                  ][si % 3]
                : group === 415
                  ? [
                      "Алгоритмы и структуры данных",
                      "Базы данных",
                      "Математический анализ",
                    ][si % 3]
                  : subjects[si],
            class_type: i % 3 === 0 ? "Лек" : i % 3 === 1 ? "Пр" : "Лб",
            classroom: `к. ${(si % 2) + 1}/${210 + si * 12}`,
            staff: [t.name],
            groups: [{ id: group, name: groups.find(g => g.id === group)?.name || "" }],
            audience_label:
              (groups.find((g) => g.id === group)?.name || "") +
              (i === 2 ? "/1" : ""),
            subgroup_id: i === 2 ? 334 : 0,
            flags: 0,
          };
        })
        .filter(
          (l) =>
            (!subgroup || !l.subgroup_id || l.subgroup_id === subgroup) &&
            (!teacher ||
              l.staff.includes(teachers.find((t) => t.id === teacher)?.name)),
        ),
    })),
  };
}

// Сессия в демонстрации: без неё блок «Сессия» невозможно ни увидеть, ни
// проверить — в сентябре вуз экзаменов не заводит.
export function demoExams(group, from) {
  const at = (days, slot) => shift(from, days);
  return [
    ["Нормальная физиология", "Экз", 12, 1, "к. 3/412"],
    ["Анатомия человека", "Зач", 15, 0, "к. 1/210"],
    ["Биохимия", "Конс", 9, 2, "к. 3/122"],
  ]
    .map(([discipline, type, days, slot, classroom], i) => ({
      id: group * 1000 + i,
      date: at(days, slot),
      number: slot + 1,
      time_id: slot + 1,
      minute_from: slots[slot][0],
      minute_to: slots[slot][1],
      time_label: slots[slot].map(clock).join(" – "),
      discipline,
      class_type: type,
      classroom,
      staff: [teachers[i % teachers.length].name],
      flags: 0,
    }))
    .sort((a, b) => a.date.localeCompare(b.date));
}

// Справочник предметов демонстрации. Идентификатор — место в списке: ссылка
// с subject=<id> обязана открываться так же, как настоящая, а имени в ней нет.
export const demoDisciplines = subjects.map((name, i) => ({ id: i + 1, name }));

// Предмет за полугодие собирается из тех же недель, что и расписание: иначе
// экран предмета показывал бы пары, которых нет в сетке.
export function demoSubject(group, discipline, subgroup, date) {
  const [from, to] = semesterOf(date);
  const out = [];
  for (let start = monday(from); start <= to; start = shift(start, 7))
    for (const day of demoWeek(group, start, subgroup).days)
      for (const lesson of day.items || [])
        if (
          lesson.discipline === discipline &&
          lesson.date >= from &&
          lesson.date <= to
        )
          out.push(lesson);
  return out.sort(
    (a, b) => a.date.localeCompare(b.date) || a.minute_from - b.minute_from,
  );
}

// Profiles and search derive from the same fictional schedule as teacher weeks.
export function demoTeacherProfile(id, date) {
  const [from, to] = semesterOf(date), subjects = new Map();
  for (let start = monday(from); start <= to; start = shift(start, 7)) {
    for (const l of items(demoWeek(39, start, 0, id)).filter(l => l.date >= from && l.date <= to)) {
      const s = subjects.get(l.discipline) || { name: l.discipline, lessons: 0, kinds: [], groups: l.groups };
      s.lessons++;
      if (!s.kinds.includes(l.class_type)) s.kinds.push(l.class_type);
      subjects.set(s.name, s);
    }
  }
  return { from, to, subjects: [...subjects.values()].sort((a, b) => b.lessons - a.lessons || a.name.localeCompare(b.name, "ru")),
    missing: false, stale: false, fetched_at: new Date().toISOString() };
}
export function demoTeacherCatalog(date, group = 0) {
  if (group && group !== 39) return [];
  return teachers.map(t => ({ ...t, subjects: [...new Set(items(demoWeek(39, monday(date), 0, t.id)).map(l => l.discipline))] }))
    .filter(t => t.subjects.length);
}
