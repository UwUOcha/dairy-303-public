import { today, monday, semesterOf, shift } from "./model.mjs";

export function createData({ demo = false, storage, fetcher = fetch,
  isOnline = () => globalThis.navigator?.onLine !== false, timeoutMs = 8000,
} = {}) {
  const cacheKey = "mp.schedule-cache.v1";
  const pending = new Map();
  const canonical = url => {
    const [path, query] = url.split("?");
    const params = new URLSearchParams(query);
    params.sort();
    return `${path}?${params}`;
  };
  function request(path, params = {}) {
    if (demo) return demoRequest(path, params);
    const url = `/api${path}?${new URLSearchParams(params)}`;
    const key = canonical(url);
    // Share only an unfinished request. A later refresh must reach the server.
    if (!pending.has(key)) {
      const task = fetchRequest(url, key).finally(() => pending.delete(key));
      pending.set(key, task);
    }
    return pending.get(key);
  }
  function readCache() {
    try {
      const value = JSON.parse(storage?.getItem(cacheKey) || "{}");
      return value && typeof value === "object" && !Array.isArray(value) ? value : {};
    } catch { return {}; }
  }
  async function fetchRequest(url, key) {
    const cached = () => {
      const cache = readCache();
      // Existing installations store URLs in the caller's parameter order.
      const entry = cache[url] || Object.entries(cache).find(([savedURL]) => canonical(savedURL) === key)?.[1];
      return entry?.value ? { ...entry.value, offline: true, stale: true, cached_at: entry.saved } : null;
    };
    if (!isOnline()) {
      const saved = cached();
      if (saved) return saved;
      throw new Error("Нет сети. Эта страница ещё не сохранена на устройстве. Откройте её при подключении к интернету.");
    }
    const controller = new AbortController();
    let timer;
    try {
      // A race bounds both headers and JSON reading, even if a browser/network
      // implementation fails to settle fetch after abort during a cold start.
      const network = (async () => {
        const response = await fetcher(url, {
          signal: controller.signal,
          cache: "no-store",
        });
        if (!response.ok) {
          const e = new Error(
            response.status === 404
              ? "Расписание не найдено. Выберите группу заново."
              : "Не удалось загрузить данные. Попробуйте ещё раз.",
          );
          e.status = response.status;
          throw e;
        }
        return response.json();
      })();
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => {
          reject(new Error("Сеть не отвечает. Попробуйте ещё раз."));
          controller.abort();
        }, timeoutMs);
      });
      const value = await Promise.race([network, timeout]);
      // Parallel week/subgroup requests must merge with the latest cache,
      // not overwrite each other with a snapshot from before their fetch.
      const latest = readCache();
      const keep = [
        [url, { value, saved: new Date().toISOString() }],
        ...Object.entries(latest).filter(
          ([k, v]) => k !== url && k.startsWith("/api/") && v?.saved,
        ),
      ].slice(0, 30);
      try {
        storage?.setItem(cacheKey, JSON.stringify(Object.fromEntries(keep)));
      } catch {}
      return value;
    } catch (error) {
      // Client errors must not resurrect a deleted group from a previous cache.
      const saved = cached();
      if ((!error.status || error.status >= 500) && saved) return saved;
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }
  return { request };
}

// Keep the group's week and its audience names together, while allowing callers
// to load independent groups concurrently.
export async function requestGroupWeek(data, { group, subgroup, monday }) {
  const [week, roster] = await Promise.all([
    data.request("/schedule/week", { group, subgroup, monday }),
    data.request("/groups/subgroups", { group }),
  ]);
  return { week, subgroups: roster.subgroups || [] };
}

async function demoRequest(path, p) {
  const {
    groups,
    teachers,
    demoWeek,
    demoGrid,
    demoExams,
    demoDisciplines,
    demoSubject,
    demoTeacherCatalog,
    demoTeacherProfile,
  } = await import("./demo.mjs");
  const group =
    groups.find((g) => g.id === Number(p.group || p.id)) || groups[0];
  const norm = (s) =>
    String(s)
      .toLocaleLowerCase("ru")
      .replace(/[^\p{L}\p{N}]/gu, "");
  if (path === "/groups/search")
    return {
      groups: groups.filter((g) => norm(g.name).includes(norm(p.q || ""))),
    };
  if (path === "/groups/get") return group;
  if (path === "/groups/departments")
    return {
      departments: [
        { id: 1, name: "Учебный факультет" },
        { id: 2, name: "Инженерно-технологический институт" },
      ],
    };
  if (path === "/groups/list")
    return p.course
      ? {
          groups: groups.filter(
            (g) =>
              g.course === Number(p.course) &&
              g.department ===
                (Number(p.department) === 1
                  ? "Учебный факультет"
                  : "Инженерно-технологический институт"),
          ),
        }
      : { courses: [2, 3] };
  if (path === "/groups/twins") return { twins: [] };
  if (path === "/groups/subgroups")
    return {
      subgroups: [
        { id: 334, name: group.name + "/1" },
        { id: 335, name: group.name + "/2" },
      ],
    };
  if (path === "/teachers/search")
    return {
      teachers: demoTeacherCatalog(p.date || today(), Number(p.group || 0)).filter(t =>
        norm(t.name).includes(norm(p.q || "")) || t.subjects.some(s => norm(s).includes(norm(p.q || "")))),
      from: semesterOf(p.date || today())[0], to: semesterOf(p.date || today())[1],
    };
  if (path === "/teachers/week")
    return {
      teacher: teachers.find((t) => t.id === Number(p.teacher)),
      profile: demoTeacherProfile(Number(p.teacher), p.monday || monday(today())),
      week: demoWeek(39, p.monday || monday(today()), 0, Number(p.teacher)),
      grid: demoGrid,
      today: today(),
      fetched_at: new Date().toISOString(),
      missing: false,
      stale: false,
      loaded_group_months: 4,
      expected_group_months: 4,
    };
  if (path === "/schedule/upcoming") {
    const from = p.date || today(), to = shift(from, 29), days = [];
    for (let at = monday(from); at <= to; at = shift(at, 7))
      days.push(...demoWeek(group.id, at, Number(p.subgroup)).days.filter(d => d.date >= from && d.date <= to));
    return { group, from, to, days, grid: demoGrid, missing: false, stale: false, fetched_at: new Date().toISOString() };
  }
  if (path === "/schedule/exams")
    return {
      group,
      items: demoExams(group.id, today()),
      from: today(),
      to: today(),
    };
  if (path === "/disciplines/get") {
    const discipline =
      demoDisciplines.find((d) => d.id === Number(p.discipline)) ||
      demoDisciplines.find((d) => !p.discipline && d.name === p.q);
    if (!discipline) throw Object.assign(new Error("Предмет не найден"), { status: 404 });
    const at = p.date || today(),
      [from, to] = semesterOf(at);
    return {
      group,
      discipline,
      items: demoSubject(group.id, discipline.name, Number(p.subgroup), at),
      from,
      to,
      months: from.slice(5, 7) === "09" ? 5 : 7,
      months_loaded: from.slice(5, 7) === "09" ? 5 : 7,
      grid: demoGrid,
      today: today(),
      fetched_at: new Date().toISOString(),
      missing: false,
      stale: false,
    };
  }
  if (path === "/changes/history") {
    const { snapshot } = await import("./changes.mjs");
    const current = snapshot(demoWeek(group.id, p.monday || monday(today()), Number(p.subgroup)));
    const intermediate = current.map((l,i) => i === 0 ? { ...l, classroom: "к. 3/412" } : l);
    if (current[0]) intermediate.push({ ...current[0], id: 987654, discipline: "Консультация по анатомии", minute_from: 980, minute_to: 1075 });
    const at = hours => new Date(Date.now() - hours*3600000).toISOString();
    const revisions = [
      { id: 103, created_at: at(1), before_at: at(5), before: intermediate, after: current },
      { id: 102, created_at: at(5), before_at: at(24), before: current, after: intermediate },
      { id: 101, created_at: at(24), before_at: at(30), before: [], after: current },
    ];
    return p.id ? revisions.find(r => r.id === Number(p.id)) : { revisions: revisions.map(({before,after,...r}) => r), retention_days: 14 };
  }
  if (path === "/changes/dates") return { dates: [], today: today() };
  if (path === "/schedule/week")
    return {
      group,
      week: demoWeek(group.id, p.monday || monday(today()), Number(p.subgroup)),
      grid: demoGrid,
      today: today(),
      fetched_at: new Date().toISOString(),
      missing: false,
      stale: false,
    };
  throw new Error("Неизвестный демонстрационный запрос");
}
