import { nextThemeValue } from "../internal/web/assets/themes.mjs";
import { demoGrid } from "../internal/web/assets/demo.mjs";
import * as ux from "../internal/web/assets/ux.mjs";
const { groupedNearby, revisionSummary } = ux;
import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import * as model from "../internal/web/assets/model.mjs";
import { revisionRows } from "../internal/web/assets/revisions.mjs";
import { createData, requestGroupWeek } from "../internal/web/assets/data.mjs";
import {
  parseClassroom,
  nearbyLessons,
  nearbyBreaks,
} from "../internal/web/assets/meetings.mjs";
import { makeBotLinks, botLink } from "../internal/web/assets/links.mjs";
import {
  createChangeHistory,
  exampleChanges,
  HISTORY_KEY,
  snapshot,
  diffLessons,
} from "../internal/web/assets/changes.mjs";

const botLinks = makeBotLinks({telegram_url: "https://t.me/example_bot", vk_url: "https://vk.me/example_bot"});

const lesson = (from, to, extra = {}) => ({
  id: 1,
  date: "2026-09-07",
  minute_from: from,
  minute_to: to,
  discipline: "Анатомия",
  ...extra,
});
const memory = () => {
  const values = new Map();
  return {
    getItem: (k) => values.get(k) || null,
    setItem: (k, v) => values.set(k, v),
    removeItem: (k) => values.delete(k),
    values,
  };
};

const response = (lessons, group = 39) => ({
  group: { id: group },
  week: { monday: "2026-09-07", days: [{ items: lessons }] },
  missing: false,
  stale: false,
});
test("schedule diff finds moves, room changes, additions and removals without noise", () => {
  const before = [
    lesson(510, 605),
    lesson(625, 720, { id: 2, classroom: "к. 2/254" }),
    lesson(740, 835, { id: 3 }),
  ];
  const after = [
    { ...before[0], date: "2026-09-08" },
    { ...before[1], classroom: "к. 2/315" },
    lesson(865, 960, { id: 4 }),
  ];
  const changes = diffLessons(
    snapshot(response(before).week),
    snapshot(response(after).week),
  );
  assert.deepEqual(changes.map((c) => c.kind).sort(), [
    "added",
    "moved",
    "removed",
    "updated",
  ]);
  const normalized = snapshot(
    response([
      { ...before[0], id: 99, time_label: "ignored", staff: ["А", "Б"] },
    ]).week,
  );
  assert.deepEqual(
    diffLessons(
      normalized,
      snapshot(
        response([
          {
            ...before[0],
            id: 100,
            staff: ["Б", "А"],
            discipline: "  Анатомия  ",
          },
        ]).week,
      ),
    ),
    [],
  );
  assert.deepEqual(
    diffLessons(
      [],
      snapshot(
        response([lesson(0, 0, { flags: 1 }), lesson(0, 0, { flags: 8 })]).week,
      ),
    ),
    [],
  );
});
test("pending changes survive reload and accumulate until acknowledged", () => {
  const storage = memory(),
    first = createChangeHistory(storage),
    base = [lesson(510, 605, { classroom: "к. 1/101" })];
  assert.equal(first.observe(response(base)).status, "first");
  const update = [{ ...base[0], classroom: "к. 2/254" }];
  const pending = first.observe(response(update));
  assert.equal(pending.changes.length, 1);
  const reopened = createChangeHistory(storage);
  assert.equal(reopened.observe(response(update)).changes.length, 1);
  const newer = reopened.observe(
    response([...update, lesson(625, 720, { id: 2 })]),
  );
  assert.equal(newer.changes.length, 2);
  assert.equal(reopened.acknowledge(pending.key, pending.token), false);
  assert.equal(reopened.acknowledge(newer.key, newer.token), true);
  assert.equal(
    reopened.observe(response([...update, lesson(625, 720, { id: 2 })])).changes
      .length,
    0,
  );
});
test("history separates groups and subgroups and never interprets partial data as cancellation", () => {
  const history = createChangeHistory(memory()),
    base = response([lesson(510, 605)]);
  history.observe(base, 334);
  assert.equal(history.observe(response([], 40), 334).status, "first");
  assert.equal(history.observe(response([]), 335).status, "first");
  for (const flags of [{ missing: true }, { stale: true }, { offline: true }])
    assert.equal(
      history.observe({ ...response([]), ...flags }, 334).status,
      "unavailable",
    );
  assert.equal(history.observe(base, 334).changes.length, 0);
  assert.equal(history.observe(response([]), 334).changes[0].kind, "removed");
});
test("history recovers from corrupt storage, remains bounded and handles storage refusal", () => {
  const storage = memory();
  storage.setItem(HISTORY_KEY, "{broken");
  const history = createChangeHistory(storage);
  assert.equal(history.observe(response([])).status, "first");
  for (let i = 1; i <= 35; i++) history.observe(response([], i));
  assert.equal(
    Object.keys(JSON.parse(storage.getItem(HISTORY_KEY))).length,
    24,
  );
  history.clear();
  assert.equal(storage.getItem(HISTORY_KEY), null);
  const blocked = {
    getItem: () => null,
    setItem() {
      throw new Error("Quota");
    },
    removeItem() {},
  };
  const transient = createChangeHistory(blocked);
  assert.equal(transient.observe(response([lesson(510, 605)])).durable, false);
  assert.equal(transient.observe(response([])).changes.length, 1);
});

test("comparison merges overlapping subgroups and excludes empty/nonstudy slots", () => {
  assert.deepEqual(
    model.freeTogether(
      [lesson(510, 605), lesson(550, 650)],
      [lesson(640, 740), lesson(800, 895)],
      { min: 30 },
    ),
    [
      [740, 800],
      [895, 1080],
    ],
  );
  assert.deepEqual(
    model.freeTogether(
      [lesson(510, 1080, { flags: 1 }), lesson(510, 1080, { flags: 8 })],
      [],
    ),
    [[510, 1080]],
  );
  assert.deepEqual(
    model.freeTogether([lesson(510, 1080, { flags: 2 })], []),
    [],
  );
  assert.deepEqual(model.freeTogether([lesson(0, 0)], []), []);
  assert.deepEqual(model.freeTogether([lesson(500, 1080)], []), []);
  assert.deepEqual(
    model.freeTogether([lesson(510, 600), lesson(630, 1080)], [], { min: 30 }),
    [[600, 630]],
  );
  assert.deepEqual(
    model.freeTogether([lesson(510, 600), lesson(630, 1080)], [], { min: 31 }),
    [],
  );
});
test("statistics counts lessons but does not double count simultaneous time", () => {
  const stats = model.statistics({
    days: [
      {
        date: "2026-09-07",
        items: [
          lesson(510, 605),
          lesson(510, 605, { id: 2 }),
          lesson(740, 835, { id: 3 }),
          lesson(980, 1075, { flags: 1 }),
        ],
      },
    ],
  });
  assert.equal(stats.count, 3);
  assert.equal(stats.minutes, 190);
  assert.equal(stats.gapMinutes, 135);
  assert.equal(stats.studyDays, 1);
});

test("day rows expose empty slots before the first lesson, not only gaps between", () => {
  const grid = {
    times: [
      { id: 1, number: 1, minute_from: 510, minute_to: 605 },
      { id: 2, number: 2, minute_from: 625, minute_to: 720 },
      { id: 3, number: 3, minute_from: 740, minute_to: 835 },
      { id: 4, number: 4, minute_from: 865, minute_to: 960 },
    ],
  };
  const day = {
    items: [
      lesson(740, 835, { id: 3, number: 3 }),
      lesson(865, 960, { id: 4, number: 4 }),
    ],
  };
  const rows = model.dayRows(day, grid);
  assert.equal(rows.length, 3);
  assert.deepEqual(rows[0].free, {
    from: 510,
    to: 720,
    slots: [1, 2],
    lead: true,
  });
  assert.equal(rows[1].lesson.id, 3);
  // Хвост дня не рисуем: после последней пары человек свободен и так.
  assert.ok(!rows.some((r, i) => i > 1 && r.free));

  const middle = model.dayRows(
    {
      items: [
        lesson(510, 605, { id: 1, number: 1 }),
        lesson(865, 960, { id: 4, number: 4 }),
      ],
    },
    grid,
  );
  assert.deepEqual(
    middle.map((r) => r.free?.slots || r.lesson.id),
    [1, [2, 3], 4],
  );
  assert.equal(middle[1].free.lead, false);

  // Пара, заведённая вузом пустой, — то же окно, а не карточка.
  const empty = model.dayRows(
    {
      items: [
        lesson(510, 605, { id: 1, number: 1, flags: 1 }),
        lesson(625, 720, { id: 2, number: 2 }),
      ],
    },
    grid,
  );
  assert.equal(empty.length, 2);
  assert.equal(empty[0].free.lead, true);
  assert.equal(empty[1].lesson.id, 2);

  // Без сетки звонков остаёмся на серверном gap_before и ничего не выдумываем.
  const blind = model.dayRows(
    { items: [lesson(740, 835, { id: 3, number: 3, gap_before: 135 })] },
    { times: [] },
  );
  assert.deepEqual(blind, [{ lesson: blind[0].lesson, gapBefore: 135 }]);
  assert.deepEqual(model.dayRows(undefined, grid), []);
});
test("the now marker sits before the first row still ahead, and never lies", () => {
  const rows = [
    { free: { from: 510, to: 605, slots: [1], lead: true } },
    { lesson: lesson(625, 720, { id: 2 }), gapBefore: 0 },
    { lesson: lesson(740, 835, { id: 3 }), gapBefore: 0 },
  ];
  assert.equal(model.nowRow(rows, 400), 0); // до начала дня
  assert.equal(model.nowRow(rows, 560), 0); // внутри утреннего окна
  assert.equal(model.nowRow(rows, 630), 1); // идёт вторая строка
  assert.equal(model.nowRow(rows, 730), 2);
  assert.equal(model.nowRow(rows, 900), -1); // день кончился, отметки нет
  // Пара с неизвестным временем не даёт границы для отметки.
  assert.equal(
    model.nowRow([{ lesson: lesson(0, 0, { id: 9 }), gapBefore: 0 }], 600),
    -1,
  );
});
test("focus card distinguishes current, upcoming, future and past weeks", () => {
  const week = {
    monday: "2026-09-07",
    days: [{ items: [lesson(510, 605), lesson(625, 720, { id: 2 })] }],
  };
  assert.equal(model.scheduleFocus(week, "2026-09-07", 550).kind, "current");
  assert.equal(model.scheduleFocus(week, "2026-09-07", 550).remaining, 55);
  assert.equal(model.scheduleFocus(week, "2026-09-07", 610).lesson.id, 2);
  assert.equal(model.scheduleFocus(week, "2026-09-07", 610).wait, 15);
  assert.equal(model.scheduleFocus(week, "2026-09-06", 600).kind, "future");
  assert.equal(model.scheduleFocus(week, "2026-09-14", 500).kind, "past");
  assert.equal(model.scheduleFocus(week, "2026-09-14", 500).lesson, null);
  assert.equal(model.scheduleFocus(week, "2026-09-07", 721).kind, "done");
  const unknown = { ...week, days: [{ items: [lesson(0, 0)] }] };
  assert.equal(model.scheduleFocus(unknown, "2026-09-07", 610).kind, "unknown");
});

test("classrooms distinguish known buildings, explicit floors and floor estimates", () => {
  for (const room of ["к. 2/254", "Корпус 2, ауд. 254", "2/254"])
    assert.deepEqual(parseClassroom(room), {
      building: "2",
      room: "254",
      floor: 2,
      floorInferred: true,
    });
  assert.equal(parseClassroom("к. 2/254, 3-й этаж").floor, 3);
  assert.equal(parseClassroom("к. 2/254, 3-й этаж").floorInferred, false);
  assert.equal(parseClassroom("к. 2/1101").floor, null);
  assert.equal(parseClassroom("к. 2").room, "");
  for (const room of [
    "254",
    "Спортзал",
    "Онлайн",
    "к. 2/254, к. 3/301",
    "2/254; 3/301",
  ])
    assert.equal(parseClassroom(room), null);
});

test("nearby lessons require close-in-time on-site classes in a known shared building", () => {
  const a = lesson(510, 605, { classroom: "к. 2/254" }),
    b = lesson(550, 650, { id: 2, classroom: "к. 2/256" });
  const match = nearbyLessons([a], [b])[0];
  assert.equal(match.kind, "floor");
  assert.equal(match.floorInferred, true);
  assert.equal(match.from, 550);
  assert.equal(match.to, 605);
  assert.equal(
    nearbyLessons([a], [{ ...b, classroom: "к. 2/254" }])[0].kind,
    "room",
  );
  assert.equal(
    nearbyLessons([a], [{ ...b, classroom: "к. 2/354" }])[0].kind,
    "building",
  );
  assert.equal(nearbyLessons([a], [a])[0].sharedLesson, true);
  for (const other of [
    { ...b, classroom: "к. 3/254" },
    { ...b, classroom: "254" },
    { ...b, flags: 4 },
    { ...b, flags: 1 },
    { ...b, date: "2026-09-08" },
    { ...b, minute_from: 636 },
    { ...b, minute_from: 0, minute_to: 0 },
  ])
    assert.deepEqual(nearbyLessons([a], [other]), []);
  const handoff = nearbyLessons([a], [{ ...b, minute_from: 625 }])[0];
  assert.equal(handoff.timing, "handoff");
  assert.deepEqual([handoff.from, handoff.to], [605, 625]);
  assert.deepEqual(nearbyBreaks([a], [{ ...b, minute_from: 625 }]), []);
});

test("nearby breaks respect both schedules, five-minute minimum and 30-minute limit", () => {
  const a = lesson(510, 605, { classroom: "к. 2/254" }),
    b = lesson(510, 605, { id: 2, classroom: "к. 2/256" });
  assert.deepEqual(
    nearbyBreaks([a], [b]).map((m) => [m.from, m.to]),
    [[605, 635]],
  );
  const next = lesson(625, 720, { id: 3, classroom: "к. 3/301" });
  assert.deepEqual(
    nearbyBreaks([a, next], [b]).map((m) => [m.from, m.to]),
    [[605, 625]],
  );
  assert.deepEqual(
    nearbyBreaks([a], [b, { ...next, minute_from: 610 }]).map((m) => [
      m.from,
      m.to,
    ]),
    [[605, 610]],
  );
  assert.deepEqual(nearbyBreaks([a], [b, { ...next, minute_from: 609 }]), []);
  assert.deepEqual(nearbyBreaks([a], [{ ...b, minute_to: 636 }]), []);
  assert.deepEqual(nearbyBreaks([a], [b, lesson(0, 0)]), []);
  assert.deepEqual(
    nearbyBreaks([a], [b, { ...next, minute_from: 605, flags: 4 }]),
    [],
  );
});
test("Monday navigation is independent of local timezone and crosses year/leap boundaries", () => {
  assert.equal(model.monday("2026-09-06"), "2026-08-31");
  assert.equal(model.shift("2026-12-28", 7), "2027-01-04");
  assert.equal(model.shift("2024-02-28", 1), "2024-02-29");
});
test("calendar uses UTC, escapes input, and folds Cyrillic at 75 bytes", () => {
  const calendar = model.ics(
    {
      days: [
        {
          items: [
            lesson(510, 605, {
              discipline:
                "Анатомия; \nBEGIN:VEVENT, " +
                "Очень длинное название ".repeat(8),
              staff: ["Соколова Е.А."],
            }),
          ],
        },
      ],
    },
    "Моя группа",
    new Date("2026-09-01T00:00:00Z"),
  );
  assert.match(calendar, /DTSTART:20260907T053000Z/);
  assert.match(calendar, /DTEND:20260907T070500Z/);
  assert.match(calendar, /Анатомия\\; \\nBEGIN:VEVENT\\,/);
  assert.equal(calendar.split("\r\nBEGIN:VEVENT\r\n").length, 2);
  assert.ok(calendar.split("\r\n").every((l) => Buffer.byteLength(l) <= 75));
});
test("preferences survive corrupt storage, honor cookie fallback and reject invalid IDs", () => {
  const storage = memory();
  storage.setItem("mp.preferences.v1", "{broken");
  assert.equal(model.readPreferences(storage, "mp_group=39").group, 39);
  storage.setItem(
    "mp.preferences.v1",
    JSON.stringify({
      group: -4,
      theme: "garbage",
      favorites: [
        { id: 0, name: "bad" },
        { id: 40, name: "Друг" },
      ],
    }),
  );
  const pref = model.readPreferences(storage, "mp_group=39");
  assert.equal(pref.group, 39);
  assert.equal(pref.theme, "system");
  assert.equal(pref.favorites.length, 1);
  assert.equal(
    model.esc('<img onerror="x">'),
    "&lt;img onerror=&quot;x&quot;&gt;",
  );
});

test("subgroup names come from the catalog instead of the whole-group label or global ID", () => {
  const l = lesson(510, 605, { subgroup_id: 334, audience_label: "ГР-22" });
  assert.equal(
    model.audienceName(l, [{ id: 334, name: "ГР-22/1" }], "ГР-22"),
    "ГР-22/1",
  );
  assert.equal(
    model.audienceName(l, [{ id: 334, name: "1" }], "ГР-22"),
    "ГР-22/1",
  );
  assert.equal(model.audienceName(l, [], "ГР-22"), "Подгруппа");
  assert.equal(
    model.audienceName({ ...l, audience_label: "ГР-22/1" }, []),
    "ГР-22/1",
  );
  assert.equal(model.audienceName({ ...l, subgroup_id: 0 }, []), "ГР-22");
});
test("network failure returns explicitly stale cache; 404 never resurrects a deleted group", async () => {
  const storage = memory();
  let status = 200;
  const api = createData({
    storage,
    fetcher: async () => {
      if (status === 0) throw new TypeError("offline");
      return {
        ok: status === 200,
        status,
        json: async () => ({ week: { days: [] }, missing: false }),
      };
    },
  });
  await api.request("/schedule/week", {
    group: 39,
    subgroup: 0,
    monday: "2026-09-07",
  });
  status = 0;
  const cached = await api.request("/schedule/week", {
    group: 39,
    subgroup: 0,
    monday: "2026-09-07",
  });
  assert.equal(cached.offline, true);
  assert.equal(cached.stale, true);
  assert.ok(cached.cached_at);
  await assert.rejects(
    api.request("/schedule/week", {
      group: 40,
      subgroup: 0,
      monday: "2026-09-07",
    }),
  );
  status = 404;
  await assert.rejects(
    api.request("/schedule/week", {
      group: 39,
      subgroup: 0,
      monday: "2026-09-07",
    }),
  );
});
test("offline cache is bounded and separates weeks and subgroups", async () => {
  const storage = memory(),
    api = createData({
      storage,
      fetcher: async () => ({ ok: true, json: async () => ({ groups: [] }) }),
    });
  for (let i = 0; i < 40; i++)
    await api.request("/groups/search", { q: String(i) });
  assert.equal(
    Object.keys(JSON.parse(storage.getItem("mp.schedule-cache.v1"))).length,
    30,
  );
});

test("concurrent responses retain both week and subgroup caches", async () => {
  const storage = memory(),
    pending = [];
  const api = createData({
    storage,
    fetcher: () => new Promise((resolve) => pending.push(resolve)),
  });
  const week = api.request("/schedule/week", { group: 39 }),
    subs = api.request("/groups/subgroups", { group: 39 });
  pending[0]({ ok: true, json: async () => ({ week: { days: [] } }) });
  await week;
  pending[1]({ ok: true, json: async () => ({ subgroups: [] }) });
  await subs;
  assert.equal(
    Object.keys(JSON.parse(storage.getItem("mp.schedule-cache.v1"))).length,
    2,
  );
});

// Application contract harness: real app/actions and data model with in-memory
// platform surfaces. No browser dependency or snapshot of implementation text.
async function appHarness({
  demo = true,
  profile = {},
  saved,
  freshness = {},
  query = "",
  hash = "#schedule",
  extra = {},
  transform = (r) => r,
  clockMinute = model.minutesNow,
  dateNow = model.today,
  offline = false,
} = {}) {
  const timers = [];
  const listeners = {},
    elements = new Map(),
    tools = new Map(),
    storage = saved || memory();
  // Настройки приложения приезжают тегом страницы, а не запросом к /api/config.
  elements.set('meta[name="app-config"]', {
    content: JSON.stringify({ demo, timezone: "Europe/Moscow", ...profile }),
  });
  const element = (id) => {
    if (!elements.has(id))
      elements.set(id, {
        innerHTML: "",
        textContent: "",
        value: "",
        open: false,
        dataset: {},
        style: { setProperty() {} },
        classList: { add() {}, remove() {} },
        showModal() {
          this.open = true;
        },
        close() {
          this.open = false;
        },
        focus() {},
        addEventListener() {},
      });
    return elements.get(id);
  };
  const location = {
    origin: "http://localhost",
    pathname: "/",
    search: query,
    hash,
    protocol: "http:",
    replace() {},
  };
  const viewport = {
    scrollY: 0, scrollX: 0, innerHeight: 800,
    addEventListener: (name, handler) => (listeners["window:" + name] = handler),
    scrollTo(options, y) {
      this.scrollY = typeof options === "number" ? y : options.top || 0;
      this.scrollX = typeof options === "number" ? options : options.left || 0;
    },
    isSecureContext: false,
  };
  const context = vm.createContext({
    ...model,
    nextThemeValue,
    ...ux,
    minutesNow: clockMinute,
    today: dateNow,
    nearbyLessons,
    nearbyBreaks,
      parseClassroom,
    botLinks,
    botLink,
    createChangeHistory,
    exampleChanges,
    snapshot,
    diffLessons,
    HISTORY_KEY,
    revisionRows,
    requestGroupWeek,
    revisionSummary,
    createData: (options) => {
      const data = createData({ ...options, isOnline: () => !offline });
      return {
        request: async (path, params) => {
          if (path in extra) return typeof extra[path] === "function" ? extra[path](params) : extra[path];
          const response = await data.request(path, params);
          return path.endsWith("/week")
            ? transform({ ...response, ...freshness })
            : response;
        },
      };
    },
    console,
    URL,
    URLSearchParams,
    Date,
    Intl,
    AbortSignal,
    Blob,
    TextEncoder,
    setTimeout,
    clearTimeout,
    setInterval: (callback, ms) => { timers.push({ callback, ms }); return timers.length; },
    localStorage: storage,
    location,
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    history: {
      replaceState(_, __, url) {
        location.search = url.search;
        location.hash = url.hash;
      },
      pushState(_, __, url) {
        location.search = url.search;
        location.hash = url.hash;
      },
    },
    window: viewport,
    document: {
      cookie: "",
      title: "",
      visibilityState: "visible",
      querySelector: element,
      documentElement: element("root"),
      modelContext: { registerTool: (t) => tools.set(t.name, t) },
      addEventListener: (name, handler) => (listeners[name] = handler),
    },
    navigator: {},
    AbortController,
    fetch: async (url) => {
      throw new Error("сеть на первой отрисовке: " + url);
    },
    initPlatform: async () => {},
    backButton: () => {},
    share: async () => "",
    install: async () => false,
  });
  const source = (
    await readFile(
      new URL("../internal/web/assets/app.js", import.meta.url),
      "utf8",
    )
  ).replace(/^import[\s\S]*?from\s+["'][^"']+["'];\s*/gm, "");
  await new vm.Script(`(async()=>{${source}\n})()`, {
    filename: "web-app.js",
  }).runInContext(context);
  const click = async (dataset) => {
    const el = { dataset, disabled: false };
    await listeners.click({
      target: {
        closest: (selector) =>
          selector === "[data-route]"
            ? dataset.route
              ? el
              : null
            : selector === "[data-action]"
              ? el
              : null,
      },
      preventDefault() {},
    });
  };
  return { element, click, tools, storage, listeners, location, viewport, timers };
}
test("application opens a real week and navigates comparison, teachers, statistics and settings", async () => {
  const app = await appHarness();
  assert.match(app.element("#app").innerHTML, /Всё по расписанию/);
  const reader = app.tools.get("read_schedule_week");
  assert.ok(reader);
  assert.equal(reader.execute({}).group.id, 39);
  assert.throws(() => reader.execute({ bad: 1 }));
  // Пустые слоты до первой пары видны в сетке недели и в списке дня.
  assert.match(app.element("#app").innerHTML, /slot-free lead/);
  await app.click({ action: "view", view: "day" });
  await app.click({
    action: "day",
    date: model.shift(model.monday(model.today()), 1),
  });
  assert.match(app.element("#app").innerHTML, /slot-free lead/);
  await app.click({ action: "view", view: "week" });
  await app.click({ route: "compare" });
  assert.match(app.element("#app").innerHTML, /proximity-card/);
  await app.click({ action: "compare-mode", mode: "breaks" });
  assert.match(app.element("#app").innerHTML, /мин после пар/);
  await app.click({ action: "compare-mode", mode: "free" });
  assert.match(app.element("#app").innerHTML, /meeting-slot/);
  await app.click({ route: "teachers" });
  assert.match(app.element("#app").innerHTML, /Соколова/);
  await app.click({ action: "teacher", id: "1" });
  assert.match(app.element("#app").innerHTML, /по загруженным группам/);
  assert.ok(reader.execute({}).week);
  await app.click({ route: "stats" });
  assert.match(app.element("#app").innerHTML, /Нагрузка по дням/);
  await app.click({ route: "settings" });
  assert.match(app.element("#app").innerHTML, /Память устройства/);
  assert.match(app.element("#app").innerHTML, /Боты всегда рядом/);
  for (const link of botLinks)
    assert.ok(app.element("#app").innerHTML.includes(link.url));
  // Telegram открывается с уже выбранной группой, ВКонтакте — как раньше.
  assert.match(app.element("#app").innerHTML, /t\.me\/[\w]+\?start=g39/);
  assert.equal(botLink(botLinks[0], 39), botLinks[0].url + "?start=g39");
  assert.equal(botLink(botLinks[0], 0), botLinks[0].url);
  assert.equal(botLink(botLinks[1], 39), botLinks[1].url);
});

test("revision mode shows both sides, unchanged context, navigation and original deleted details", async () => {
  const app = await appHarness();
  assert.match(app.element("#app").innerHTML, /role="switch" aria-checked="false"/);
  await app.click({ action: "revisions-toggle" });
  let html = app.element("#app").innerHTML;
  assert.match(html, /revision-added/);
  assert.match(html, /revision-removed/);
  assert.match(html, /revision-unchanged/);
  assert.match(html, /Последние изменения/);
  assert.match(html, /08:30 – 10:05<span>1<\/span>/);
  assert.match(html, /16:20 – 17:55<span>5<\/span>/);
  assert.doesNotMatch(html, /summary-row|now-line/);
  await app.click({ action: "revision-lesson", id: "987654", kind: "removed" });
  assert.match(app.element("#detail").innerHTML, /Консультация по анатомии/);
  assert.match(app.element("#detail").innerHTML, /Было до правки/);
  await app.click({ action: "revision-step", step: "1" });
  assert.match(app.element("#app").innerHTML, /Предыдущие изменения/);
  assert.match(app.element("#app").innerHTML, /#102/);
  await app.click({ action: "revision-latest" });
  assert.match(app.element("#app").innerHTML, /Последние изменения/);
  await app.click({ action: "view", view: "day" });
  assert.match(app.element("#app").innerHTML, /revision-day-list/);
  await app.click({ action: "revisions-toggle" });
  assert.doesNotMatch(app.element("#app").innerHTML, /revision-card-mark/);
});

test("subgroup cards and details use the correct suffix even when upstream labels the whole group", async () => {
  const app = await appHarness({
    transform: (r) => {
      if (r.group)
        for (const d of r.week.days)
          for (const l of d.items || [])
            if (l.subgroup_id) l.audience_label = r.group.name;
      return r;
    },
  });
  assert.match(app.element("#app").innerHTML, /Лабораторная · ГР-22\/1/);
  const l = model
    .items(app.tools.get("read_schedule_week").execute({}).week)
    .find((l) => l.subgroup_id);
  await app.click({ action: "lesson", id: String(l.id) });
  assert.match(app.element("#detail").innerHTML, /ГР-22\/1/);
  await app.listeners.change({ target: { id: "subgroup", value: "335" } });
  assert.ok(
    model
      .items(app.tools.get("read_schedule_week").execute({}).week)
      .every((l) => !l.subgroup_id || l.subgroup_id === 335),
  );
});

test("incomplete schedules still show location evidence but never promise a shared break", async () => {
  const app = await appHarness({ freshness: { missing: true, stale: true } });
  await app.click({ route: "compare" });
  assert.match(app.element("#app").innerHTML, /proximity-card/);
  assert.match(
    app.element("#app").innerHTML,
    /Совпадение места может быть неактуально/,
  );
  await app.click({ action: "compare-mode", mode: "breaks" });
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="meeting"/);
  await app.click({ action: "compare-mode", mode: "free" });
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="meeting"/);
});
test("a shared date link opens that day and stays in the address bar", async () => {
  const date = model.shift(model.monday(model.today()), 1);
  const app = await appHarness({ query: `?group=39&date=${date}` });
  const html = app.element("#app").innerHTML;
  assert.match(html, /day-tabs/);
  // Во вторник выбранный день также отмечен как сегодняшний.
  assert.match(html, new RegExp(`data-date="${date}" class="active(?: today)?"`));
  assert.ok(app.location.search.includes(`date=${date}`));
  // Мусор в параметре не переключает вид и не ломает неделю.
  const junk = await appHarness({ query: "?group=39&date=не-дата" });
  assert.doesNotMatch(junk.element("#app").innerHTML, /day-tabs/);
});
test("server history preserves empty to filled to empty on first visit", async () => {
  const date = model.monday(model.today()), l = {...lesson(510,605), date};
  const revisions = [
    {id:2, created_at:new Date().toISOString(), before_at:new Date(Date.now()-3600000).toISOString(), before:[l], after:[]},
    {id:1, created_at:new Date(Date.now()-3600000).toISOString(), before_at:new Date(Date.now()-7200000).toISOString(), before:[], after:[l]},
  ];
  const app = await appHarness({extra:{"/changes/history": p => p.id ? revisions.find(r=>r.id===p.id) : {revisions}}});
  await app.click({action:"revisions-toggle"});
  assert.match(app.element("#app").innerHTML, /− Было/);
  await app.click({action:"revision-step",step:"1"});
  assert.match(app.element("#app").innerHTML, /\+ Стало/);
  assert.doesNotMatch(app.element("#app").innerHTML, /правку могли откатить/);
});
test("history errors are retryable and empty history labels the live schedule", async () => {
  let fail = true;
  const app = await appHarness({extra:{"/changes/history": () => { if(fail) throw new Error("offline"); return {revisions:[]}; }}});
  await app.click({action:"revisions-toggle"});
  assert.match(app.element("#app").innerHTML, /Не удалось загрузить историю/);
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="lesson"/);
  fail=false;
  await app.click({action:"revisions-reload"});
  assert.match(app.element("#app").innerHTML, /Пока без снимков правок/);
  assert.match(app.element("#app").innerHTML, /data-action="lesson"/);
});

test("the session screen appears only when there is a session, and lists it in order", async () => {
  assert.equal(model.sessionKind("Экз"), "Экзамен");
  assert.equal(model.sessionKind("Диф. зач"), "Дифзачёт");
  assert.equal(model.sessionKind("Лек"), "");
  assert.equal(model.sessionKind("Инд"), "");
  assert.equal(model.daysUntil("2026-09-10", "2026-09-07"), 3);
  assert.equal(model.daysUntil("2026-09-07", "2026-09-07"), 0);
  const day = (n) => `${n} ${model.plural(n, "день", "дня", "дней")}`;
  assert.deepEqual([1, 2, 5, 11, 12, 14, 21, 22, 25, 101, 111].map(day), [
    "1 день",
    "2 дня",
    "5 дней",
    "11 дней",
    "12 дней",
    "14 дней",
    "21 день",
    "22 дня",
    "25 дней",
    "101 день",
    "111 дней",
  ]);

  const app = await appHarness();
  const html = app.element("#app").innerHTML;
  assert.match(html, /session-banner/);
  // Ближайшее испытание — консультация через 9 дней, а не экзамен через 12.
  assert.match(html, /Биохимия/);
  await app.click({ action: "session-open" });
  const dialog = app.element("#detail").innerHTML;
  assert.ok(
    dialog.indexOf("Биохимия") < dialog.indexOf("Нормальная физиология"),
    "испытания должны идти по возрастанию даты",
  );
  assert.match(dialog, /Консультация/);
  assert.match(dialog, /Зачёт/);

  // Вне сессии блок исчезает целиком, а не висит пустым.
  const quiet = await appHarness({ extra: { "/schedule/exams": {} } });
  assert.doesNotMatch(quiet.element("#app").innerHTML, /session-banner/);
});
test("group selection, favorites and view changes persist; production does not open fictional schedule", async () => {
  const app = await appHarness();
  await app.click({ action: "pick", target: "group" });
  assert.equal(app.element("#picker").open, true);
  await app.click({ action: "choose-group", id: "262" });
  assert.equal(app.tools.get("read_schedule_week").execute({}).group.id, 262);
  await app.click({ action: "favorite" });
  await app.click({ action: "view", view: "day" });
  const saved = JSON.parse(app.storage.getItem("demo.mp.preferences.v1"));
  assert.equal(saved.group, 262);
  assert.equal(saved.view, "day");
  assert.equal(saved.favorites[0].id, 262);
  const restored = await appHarness({ saved: app.storage });
  assert.equal(
    restored.tools.get("read_schedule_week").execute({}).group.id,
    262,
  );
  const live = await appHarness({ demo: false });
  assert.match(live.element("#app").innerHTML, /Найти мою группу/);
  assert.equal(live.tools.get("read_schedule_week").execute({}).week, null);
});

test("day tabs start adjacent weeks on Monday and return to today", async () => {
  const now = () => "2026-09-12"; // Saturday: the user's regression example.
  const app = await appHarness({dateNow: now});
  await app.click({ action: "view", view: "day" });
  const monday = model.monday(now());
  const tabs = () =>
    app
      .element("#app")
      .innerHTML.match(/<div class="day-tabs"[\s\S]*?<\/div>/)[0];
  // Воскресенья в неделе нет, значит и вкладки по нему быть не должно:
  // раньше вкладки строились из семи дат подряд и вели в заведомо пустой день.
  if (now() !== model.shift(monday, 6)) assert.doesNotMatch(tabs(), new RegExp(`data-date="${model.shift(monday, 6)}"`));
  assert.match(tabs(), new RegExp(`data-date="${model.shift(monday, 5)}"`));
  const tuesday = model.shift(monday, 1);
  await app.click({ action: "day", date: tuesday });
  await app.click({ action: "next" });
  assert.match(
    app.element("#app").innerHTML,
    new RegExp(`data-date="${model.shift(monday, 7)}" class="active"`),
  );
  assert.match(app.location.search, new RegExp(`date=${model.shift(monday,7)}`));
  await app.click({action:"prev"});
  assert.match(app.location.search, new RegExp(`date=${now()}`));
  await app.click({action:"prev"});
  assert.match(app.location.search, new RegExp(`date=${model.shift(monday,-7)}`));
  await app.click({action:"next"});
  assert.match(app.location.search, new RegExp(`date=${now()}`));
});

test("the return-to-today control appears only when there is somewhere to return", async () => {
  const app = await appHarness();
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="today"/);
  await app.click({ action: "next" });
  assert.match(app.element("#app").innerHTML, /data-action="today"/);
  await app.click({ action: "today" });
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="today"/);
  // В дневном виде «сегодня» — это ещё и день, а не только неделя. В
  // воскресенье, которого в неделе нет, им считается ближайший день недели.
  await app.click({ action: "view", view: "day" });
  await app.click({ action: "prev" });
  assert.match(app.element("#app").innerHTML, /data-action="today"/);
  await app.click({ action: "today" });
  assert.doesNotMatch(app.element("#app").innerHTML, /data-action="today"/);
});

test("group, subgroup and the star stand together; the schedule keeps no export or print buttons", async () => {
  const app = await appHarness();
  const html = app.element("#app").innerHTML;
  assert.match(
    html,
    /group-controls[\s\S]*?id="subgroup"[\s\S]*?data-action="favorite"/,
  );
  assert.doesNotMatch(html, /data-action="print"/);
  assert.doesNotMatch(html, /data-action="export"/);
  // Подгруппа продолжает работать оттуда, где теперь стоит.
  await app.listeners.change({ target: { id: "subgroup", value: "335" } });
  assert.ok(
    model
      .items(app.tools.get("read_schedule_week").execute({}).week)
      .every((l) => !l.subgroup_id || l.subgroup_id === 335),
  );
  // Экспорт остался в настройках, где ему и место.
  await app.click({ route: "settings" });
  assert.match(app.element("#app").innerHTML, /data-action="export"/);
});

test("subject summary splits kinds, teachers and rooms and counts only finished lessons as done", () => {
  const items = [
    lesson(510, 605, {
      id: 1,
      class_type: "Лек",
      classroom: "к. 1/210",
      staff: ["Иванов И."],
    }),
    lesson(625, 720, {
      id: 2,
      class_type: "Лб",
      classroom: "к. 1/210",
      staff: ["Петрова А."],
    }),
    // Пустой слот — не занятие: считать его парой значит обещать лишнее.
    lesson(510, 605, {
      id: 3,
      date: "2026-09-14",
      class_type: "Лек",
      flags: 1,
      staff: ["Иванов И."],
    }),
    lesson(510, 605, {
      id: 4,
      date: "2026-09-21",
      class_type: "Лек",
      classroom: "к. 1/210",
      staff: ["Иванов И."],
    }),
  ];
  const sum = model.subjectSummary(items, "2026-09-07", 700);
  assert.equal(sum.total, 3);
  // Первая пара кончилась в 10:05, вторая ещё идёт: «осталось» считается по
  // концу пары, а не по началу её дня.
  assert.equal(sum.done, 1);
  assert.equal(sum.left, 2);
  assert.equal(sum.next.id, 2);
  assert.equal(sum.minutes, 285);
  assert.deepEqual(
    sum.kinds.map((k) => [k.label, k.count]),
    [
      ["Лекция", 2],
      ["Лабораторная", 1],
    ],
  );
  assert.deepEqual(
    sum.teachers.map((t) => [t.name, t.count]),
    [
      ["Иванов И.", 2],
      ["Петрова А.", 1],
    ],
  );
  assert.deepEqual(sum.rooms[0], ["к. 1/210", 3]);
  assert.deepEqual(sum.upcoming.map(l => l.id), [2, 4]);
  assert.deepEqual(model.semesterOf("2026-09-06"), ["2026-09-01", "2027-01-31"]);
  assert.deepEqual(model.semesterOf("2027-01-15"), ["2026-09-01", "2027-01-31"]);
  assert.deepEqual(model.semesterOf("2027-03-02"), ["2027-02-01", "2027-08-31"]);
  assert.equal(model.lessonKind("Лек").key, "lecture");
  assert.equal(model.lessonKind("Экз").label, "Экзамен");
  assert.equal(model.lessonKind("").label, "Занятие");
});

test("the subject screen opens from a lesson, keeps the link and returns to the schedule", async () => {
  const app = await appHarness();
  const first = model.items(
    app.tools.get("read_schedule_week").execute({}).week,
  )[0];
  await app.click({ action: "lesson", id: String(first.id) });
  assert.match(app.element("#detail").innerHTML, /data-action="subject"/);
  await app.click({ action: "subject", name: first.discipline });
  const html = app.element("#app").innerHTML;
  assert.ok(html.includes(first.discipline), "предмет назван в шапке");
  assert.match(html, /По опубликованному расписанию/);
  assert.match(html, /kind-bar/);
  assert.match(html, /subject-lesson/);
  // Полугодие, а не неделя: занятий заметно больше, чем в сетке.
  assert.ok(
    (html.match(/subject-lesson/g) || []).length > 7,
    "лента предмета собрана за полугодие",
  );
  // Ссылкой делятся уже с найденным идентификатором.
  assert.match(app.location.search, /subject=\d+/);
  await app.click({ action: "schedule-back" });
  assert.match(app.element("#app").innerHTML, /Всё по расписанию/);
  assert.doesNotMatch(app.location.search, /subject=/);
  // Из статистики — тот же экран.
  await app.click({ route: "stats" });
  assert.match(app.element("#app").innerHTML, /data-action="subject"/);
});

test("a shared subject link opens the subject itself, not the week", async () => {
  const app = await appHarness({ query: "?group=39&subject=2" });
  const html = app.element("#app").innerHTML;
  assert.ok(html.includes("Анатомия человека"));
  assert.match(html, /Занятия по предмету/);
});

test("all themes cycle from the header, sync with settings and survive reload", async () => {
  assert.equal(nextThemeValue("system", "dark"), "night");
  assert.equal(nextThemeValue("system", "light"), "dark");
  const saved = memory();
  const app = await appHarness({ saved });
  await app.click({ route: "settings" });
  assert.match(app.element("#app").innerHTML, /value="night"[^>]*>Звёздная ночь/);
  for (const [theme, color] of [
    ["dark", "#202620"], ["night", "#121525"],
    ["light", "#f6f7f4"], ["dark", "#202620"],
  ]) {
    await app.click({ action: "theme" });
    assert.equal(app.element("root").dataset.theme, theme === "system" ? "light" : theme);
    assert.equal(app.element('meta[name="theme-color"]').content, color);
    assert.equal(app.element("#theme-select").value, theme);
    assert.equal(JSON.parse(saved.getItem("demo.mp.preferences.v1")).theme, theme);
  }
  await app.listeners.change({ target: { id: "theme-select", value: "night" } });
  assert.match(app.element("#theme-toggle").ariaLabel, /Звёздная ночь.*Светлая/);
  const reopened = await appHarness({ saved });
  assert.equal(reopened.element("root").dataset.theme, "night");
  assert.equal(saved.getItem("mp.preferences.v1"), null);
});

test("first paint restores the right theme without borrowing another site's preferences", async () => {
  const source = await readFile(new URL("../internal/web/assets/theme.js", import.meta.url), "utf8");
  function boot({ demo = false, saved = memory(), dark = false } = {}) {
    const root = { dataset: {} }, meta = {};
    new vm.Script(source).runInNewContext({
      localStorage: saved,
      matchMedia: () => ({ matches: dark }),
      document: {
        documentElement: root,
        querySelector: (selector) => selector.includes("app-config") ? { content: JSON.stringify({ demo }) } : meta,
      },
    });
    return { theme: root.dataset.theme, color: meta.content };
  }
  const saved = memory();
  saved.setItem("mp.preferences.v1", JSON.stringify({ theme: "night" }));
  saved.setItem("demo.mp.preferences.v1", JSON.stringify({ theme: "light" }));
  assert.deepEqual(boot({ saved }), { theme: "night", color: "#121525" });
  assert.deepEqual(boot({ saved, demo: true }), { theme: "light", color: "#f6f7f4" });
  saved.removeItem("mp.preferences.v1");
  assert.equal(boot({ saved, dark: true }).theme, "dark");
  saved.setItem("mp.preferences.v1", "broken");
  assert.equal(boot({ saved }).theme, "light");
  assert.equal(boot({ saved: { getItem() { throw new Error("blocked"); } }, dark: true }).theme, "dark");
});

test("switching day and week keeps the address and reloaded view in sync", async () => {
  const app = await appHarness({ query: "?group=39&date=2026-09-07" });
  await app.click({ action: "day", date: "2026-09-08" });
  assert.equal(new URLSearchParams(app.location.search).get("date"), "2026-09-08");
  await app.click({ action: "view", view: "week" });
  assert.equal(new URLSearchParams(app.location.search).has("date"), false);
  const reopened = await appHarness({ saved: app.storage, query: app.location.search });
  assert.match(reopened.element("#app").innerHTML, /class="week-grid/);
  await reopened.click({ action: "view", view: "day" });
  assert.ok(new URLSearchParams(reopened.location.search).has("date"));
});

test("building transfer hints default to off for new, old and malformed preferences", () => {
  const saved = memory();
  assert.equal(model.readPreferences(saved).showTransfers, false);
  for (const value of [{ theme: "night" }, { showTransfers: "true" }, { showTransfers: 1 }, { showTransfers: null }]) {
    saved.setItem("mp.preferences.v1", JSON.stringify(value));
    assert.equal(model.readPreferences(saved).showTransfers, false);
  }
  saved.setItem("mp.preferences.v1", "broken");
  assert.equal(model.readPreferences(saved).showTransfers, false);
});

test("building transfer hints are optional in week and day views and survive reload", async () => {
  const saved = memory();
  let app = await appHarness({ saved });
  const hints = /class="transfer"/;
  const html = () => app.element("#app").innerHTML;
  const monday = model.monday(model.today());
  assert.doesNotMatch(html(), hints);
  await app.click({ action: "view", view: "day" });
  await app.click({ action: "day", date: monday });
  assert.doesNotMatch(html(), hints);
  await app.click({ route: "settings" });
  assert.match(html(), /id="show-transfers"[\s\S]*?value="off" selected/);
  await app.listeners.change({ target: { id: "show-transfers", value: "on" } });
  assert.equal(JSON.parse(saved.getItem("demo.mp.preferences.v1")).showTransfers, true);
  await app.click({ route: "schedule" });
  assert.match(html(), hints);
  await app.click({ action: "view", view: "week" });
  assert.match(html(), hints);

  app = await appHarness({ saved });
  assert.match(html(), hints);
  await app.click({ route: "settings" });
  assert.match(html(), /id="show-transfers"[\s\S]*?value="on" selected/);
  await app.listeners.change({ target: { id: "show-transfers", value: "off" } });
  await app.click({ route: "schedule" });
  assert.doesNotMatch(html(), hints);
  await app.click({ action: "view", view: "day" });
  await app.click({ action: "day", date: monday });
  assert.doesNotMatch(html(), hints);
  app = await appHarness({ saved });
  assert.doesNotMatch(html(), hints);
  assert.equal(JSON.parse(saved.getItem("demo.mp.preferences.v1")).showTransfers, false);
  assert.equal(saved.getItem("mp.preferences.v1"), null);
});

test("subject totals sort records, merge parallel times and keep unknown times pending", () => {
  const source = [
    lesson(0, 0, { id: 9, date: "2026-09-08", class_type: "Экз" }),
    lesson(625, 720, { id: 3, subgroup_id: 335 }),
    lesson(510, 605, { id: 1, staff: ["Иванов", "Иванов"] }),
    lesson(0, 0, { id: 4 }),
    lesson(625, 720, { id: 2, subgroup_id: 334 }),
    lesson(700, 800, { id: 5, flags: 8 }),
  ];
  const sum = model.subjectSummary(source, "2026-09-07", 700);
  assert.equal(sum.total, 5);
  assert.equal(sum.minutes, 190);
  assert.equal(sum.unknownTime, 2);
  assert.equal(sum.current.id, 2);
  assert.equal(sum.next.id, 2);
  assert.deepEqual(sum.past.map(l => l.id), [1]);
  assert.deepEqual(sum.assessments.map(l => l.id), [9]);
  assert.equal(sum.teachers[0].count, 1);
  assert.equal(source[0].id, 9, "sorting must not mutate the response");
});

test("subject links retain the lesson semester across a boundary and a reload", async () => {
  const app = await appHarness({ query: "?group=39&week=2026-08-31" });
  await app.click({ action: "subject", name: "Анатомия человека", date: "2026-09-01" });
  assert.match(app.element("#app").innerHTML, /31 января 2027/);
  assert.equal(new URLSearchParams(app.location.search).get("subject_date"), "2026-09-01");
  const shared = await appHarness({ query: app.location.search });
  assert.match(shared.element("#app").innerHTML, /31 января 2027/);
  await shared.click({ action: "schedule-back" });
  assert.equal(new URLSearchParams(shared.location.search).get("week"), "2026-08-31");
  assert.doesNotMatch(shared.location.search, /subject/);
  const historic = await appHarness({ query: "?group=39&week=2026-05-04&subject=2" });
  assert.match(historic.element("#app").innerHTML, /1 февраля 2026/);
});

test("subject loads independently of week/session endpoints and uses its own assessments", async () => {
  const next = model.shift(model.today(), 2);
  const response = {
    group: { id: 39, name: "ГР-22" }, discipline: { id: 2, name: "Анатомия" },
    from: model.today(), to: model.shift(next, 10), months: 5, months_loaded: 1,
    items: [lesson(510, 605, { id: 999, date: next, class_type: "Экз", classroom: "к. 7/777" })],
  };
  const fail = () => { throw new Error("Unrelated request"); };
  const app = await appHarness({ query: "?group=39&subject=2", extra: {
    "/disciplines/get": response, "/schedule/week": fail, "/schedule/exams": fail,
  } });
  const html = app.element("#app").innerHTML;
  assert.match(html, /Загружено месяцев: 1 из 5/);
  assert.match(html, /Экзамен/);
  assert.match(html, /к\. 7\/777/);
  assert.doesNotMatch(html, /Все занятия полугодия уже прошли|Учебных часов/);
  await app.click({ action: "lesson", id: "999", date: next });
  assert.match(app.element("#detail").innerHTML, /к\. 7\/777/);
});

test("subject filters exclude placeholders, paginate and open the exact recurring lesson", async () => {
  const start = model.shift(model.today(), 1);
  const list = Array.from({length: 35}, (_, i) => lesson(510, 605, {
    id: 42, date: model.shift(start, i), classroom: `к. 1/${100 + i}`,
  }));
  const app = await appHarness({ query: "?group=39&subject=2", extra: {
    "/disciplines/get": {
      group: {id: 39, name: "ГР-22"}, discipline: {id: 2, name: "Анатомия"},
      from: model.shift(start, -10), to: model.shift(start, 40), months: 5, months_loaded: 1,
      items: [...list, lesson(510, 605, {id: 66, flags: 1}), lesson(510, 605, {id: 77, date: "2000-01-01"})],
    },
  } });
  assert.doesNotMatch(app.element("#app").innerHTML, /data-id="66"|data-id="77"/);
  assert.match(app.element("#app").innerHTML, /Показать ещё/);
  await app.click({ action: "subject-more" });
  assert.match(app.element("#app").innerHTML, /к\. 1\/131/);
  await app.click({ action: "lesson", id: "42", date: model.shift(start, 20) });
  assert.match(app.element("#detail").innerHTML, /к\. 1\/120/);
  await app.click({ action: "subject-filter", filter: "past" });
  const feed = app.element("#app").innerHTML.split('<section class="subject-feed">')[1];
  assert.match(feed, /data-id="77"/);
  assert.doesNotMatch(feed.split('<aside')[0], /data-id="42"|data-id="66"/);
});

test("subject failures distinguish missing subjects from server failures and retry", async () => {
  let failing = true;
  const api = createData({demo: true});
  const app = await appHarness({ query: "?group=39&subject_name=Анатомия+человека", extra: {
    "/disciplines/get": async p => {
      if (failing) throw Object.assign(new Error("Unavailable"), {status: 503});
      return api.request("/disciplines/get", p);
    },
  } });
  assert.match(app.element("#app").innerHTML, /Не удалось загрузить предмет/);
  assert.match(app.location.search, /subject_name=/);
  failing = false;
  await app.click({action: "reload"});
  assert.match(app.location.search, /subject=2/);
  assert.match(app.element("#app").innerHTML, /Занятия по предмету/);
  const missing = await appHarness({ query: "?group=39&subject=999999" });
  assert.match(missing.element("#app").innerHTML, /Предмет не найден/);
});

test("subject navigation and browser back restore the right screen and semester", async () => {
  const app = await appHarness({ query: "?group=39&subject=2&subject_date=2027-02-01" });
  await app.click({ route: "schedule" });
  assert.match(app.element("#app").innerHTML, /Всё по расписанию/);
  assert.doesNotMatch(app.location.search, /subject/);
  app.location.search = "?group=39&week=2026-08-31&subject=2&subject_date=2026-09-01";
  await app.listeners["window:popstate"]();
  // popstate returns the load promise so restoration can be awaited.
  assert.match(app.element("#app").innerHTML, /31 января 2027/);
});

test("an offline subject reuses its cached response and labels it stale", async () => {
  const storage = memory();
  let online = true;
  const api = createData({ storage, fetcher: async () => {
    if (!online) throw new TypeError("offline");
    return {ok: true, json: async () => ({discipline: {id: 2}, items: []})};
  }});
  await api.request("/disciplines/get", {group: 39, discipline: 2});
  online = false;
  const cached = await api.request("/disciplines/get", {group: 39, discipline: 2});
  assert.equal(cached.discipline.id, 2);
  assert.equal(cached.offline, true);
  assert.equal(cached.stale, true);
  await assert.rejects(api.request("/disciplines/get", {group: 40, discipline: 2}));
});


test("a subject lesson opens the whole day with its exact date", async () => {
  const date = "2027-01-18";
  const app = await appHarness({query: "?group=39&subject=2&subject_date=2026-09-01"});
  await app.click({action: "lesson-day", date});
  assert.match(app.element("#app").innerHTML, /day-tabs/);
  assert.equal(new URLSearchParams(app.location.search).get("date"), date);
  assert.doesNotMatch(app.location.search, /subject/);
});

test("changing a subject subgroup filters its lessons and preserves the subject date", async () => {
  const app = await appHarness({query: "?group=39&subject=1&subject_date=2027-01-01"});
  await app.listeners.change({target: {id: "subgroup", value: "335"}});
  assert.equal(new URLSearchParams(app.location.search).get("subgroup"), "335");
  assert.equal(new URLSearchParams(app.location.search).get("subject_date"), "2027-01-01");
  assert.match(app.element("#app").innerHTML, /31 января 2027/);
  assert.doesNotMatch(app.element("#app").innerHTML, /Занятия всех подгрупп считаются отдельно/);
});

test("teacher week summary filters subjects, preserves flow groups and ignores unknown times for next", () => {
  const a = { id: 39, name: "Группа А" }, b = { id: 40, name: "Группа Б" };
  const week = { days: [{ items: [
    lesson(740, 835, { id: 3, discipline: "Биохимия", groups: [b], classroom: "к. 1/200" }),
    lesson(510, 605, { id: 1, groups: [a, b], classroom: "к. 1/100" }),
    lesson(0, 0, { id: 2, groups: [a] }),
    lesson(625, 720, { id: 4, flags: 1 }),
    lesson(625, 720, { id: 5, flags: 8 }),
    lesson(865, 960, { id: 6, groups: [a], flags: 4, classroom: "к. 9/999" }),
  ] }] };
  const sum = model.teacherWeekSummary(week, "Анатомия", "2026-09-07", 550);
  assert.equal(sum.lessons.length, 3);
  assert.equal(sum.days, 1);
  assert.equal(sum.groups.length, 2);
  assert.equal(sum.next.id, 1);
  assert.equal(sum.current, true);
  assert.ok(sum.rooms.some(r => /Онлайн/.test(r.name)));
  assert.ok(!sum.rooms.some(r => /999/.test(r.name)));
  assert.equal(model.teacherWeekSummary(week, "Анатомия", "2026-09-07", 605).next.id, 6);
  assert.equal(model.teacherWeekSummary(week, "Анатомия", "2026-09-08", 0).next, null);
  assert.equal(model.teacherWeekSummary(week, "Нет предмета").lessons.length, 0);
});

test("teacher profile filters the agenda, preserves links and leaves the selected group untouched", async () => {
  const app = await appHarness();
  await app.click({ route: "teachers" });
  assert.match(app.element("#app").innerHTML, /Фамилия или предмет/);
  assert.match(app.element("#app").innerHTML, /Нормальная физиология/);
  await app.click({ action: "teacher", id: "1" });
  await app.click({ action: "teacher-subject", name: "Биохимия" });
  const html = app.element("#app").innerHTML;
  assert.match(html, /Что ведёт/);
  assert.match(html, /teacher-filter/);
  assert.match(html, /моя группа/);
  assert.doesNotMatch(html, /slot-free|мин · окно/);
  const cards = [...html.matchAll(/<button class="teacher-lesson"[\s\S]*?<\/button>/g)].map(m => m[0]);
  assert.ok(cards.length > 0);
  assert.ok(cards.every(card => card.includes("Биохимия")));
  assert.ok(cards.every(card => !card.includes("Соколова")));
  assert.equal(new URLSearchParams(app.location.search).get("teacher_subject"), "Биохимия");
  const oldWeek = new URLSearchParams(app.location.search).get("week");
  await app.click({ action: "next" });
  assert.equal(new URLSearchParams(app.location.search).get("week"), model.shift(oldWeek, 7));
  assert.equal(new URLSearchParams(app.location.search).get("teacher_subject"), "Биохимия");
  const restored = await appHarness({ query: app.location.search, hash: "#teachers" });
  assert.match(restored.element("#app").innerHTML, /teacher-filter/);
  await restored.click({ action: "teacher-subject" });
  assert.equal(new URLSearchParams(restored.location.search).has("teacher_subject"), false);
  await app.click({ action: "all-teachers" });
  assert.equal(new URLSearchParams(app.location.search).has("teacher"), false);
  await app.click({ action: "teacher", id: "3" });
  assert.doesNotMatch(app.element("#app").innerHTML, /teacher-subject-all|Выберите предмет, чтобы/);
  await app.click({ route: "schedule" });
  assert.equal(app.tools.get("read_schedule_week").execute({}).group.id, 39);
});

test("teacher search supports subject queries, personal scope, stale results and debounced races", async () => {
  const requests = [];
  let resolveOld;
  const app = await appHarness({ extra: { "/teachers/search": params => {
    requests.push(params);
    if (params.q === "старый" && params.group) return new Promise(resolve => { resolveOld = resolve; });
    return { teachers: [{ id: 1, name: params.group ? "Преподаватель моей группы" : "Результат поиска", subjects: [params.q || "Анатомия"] }], offline: true };
  } } });
  await app.click({ route: "teachers" });
  app.listeners.input({ target: { id: "teacher-search", value: "Биохимия" } });
  await new Promise(r => setTimeout(r, 300));
  assert.equal(requests.at(-1).q, "Биохимия");
  assert.match(app.element("#teacher-results").innerHTML, /Биохимия/);
  assert.match(app.element("#teacher-results").innerHTML, /сохранённые результаты/);
  await app.click({ action: "teacher-scope", scope: "mine" });
  assert.equal(requests.at(-1).group, 39);
  assert.equal(requests.at(-1).q, "Биохимия");
  assert.equal(new URLSearchParams(app.location.search).get("teacher_scope"), "mine");
  app.listeners.input({ target: { id: "teacher-search", value: "старый" } });
  await new Promise(r => setTimeout(r, 300));
  await app.click({ action: "teacher-scope", scope: "all" });
  resolveOld({ teachers: [{ id: 2, name: "УСТАРЕВШИЙ РЕЗУЛЬТАТ", subjects: [] }] });
  await new Promise(r => setTimeout(r, 0));
  assert.doesNotMatch(app.element("#teacher-results").innerHTML, /УСТАРЕВШИЙ/);
  assert.equal(requests.at(-1).group, undefined);
});


test("empty teacher weeks retain profile subjects, disclose incomplete coverage and escape data", async () => {
  const dangerous = '<img src=x onerror="alert(1)">';
  const app = await appHarness({ extra: { "/teachers/week": {
    teacher: { id: 99, name: dangerous }, week: { days: [] }, missing: true,
    profile: { from: "2026-09-01", to: "2027-01-31", missing: true, stale: true,
      subjects: [{ name: dangerous, kinds: ["Лек"], groups: [{ id: 40, name: dangerous }] }] },
  } } });
  await app.click({ route: "teachers" });
  await app.click({ action: "teacher", id: "99" });
  let html = app.element("#app").innerHTML;
  assert.match(html, /Полугодие загружено частично/);
  assert.match(html, /На этой неделе занятий не найдено/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<img|class="teacher-next"/);
  await app.click({ action: "teacher-subject", name: dangerous });
  html = app.element("#app").innerHTML;
  assert.match(html, /По предмету нет занятий/);
  assert.match(html, /Сбросить/);
  assert.doesNotMatch(html, /<img/);
});

test("revision patches retain moved lessons on both dates and ignore recreated IDs", () => {
  const a = lesson(510,605,{id:1}), unchanged = lesson(625,720,{id:2});
  const rows = revisionRows({before:[a,unchanged],after:[{...a,date:model.shift(a.date,1)}, {...unchanged,id:99}]});
  assert.equal(rows.filter(l=>l.revisionKind==="added").length,1);
  assert.equal(rows.filter(l=>l.revisionKind==="removed").length,1);
  assert.equal(rows.filter(l=>l.revisionKind==="unchanged").length,1);
  assert.equal(rows.find(l=>l.revisionKind==="removed").date,a.date);
  assert.equal(rows.find(l=>l.revisionKind==="added").date,model.shift(a.date,1));
});

test("latest revision follows refresh while an explicitly selected archive stays pinned", async () => {
  const stamp = new Date().toISOString();
  let versions = [{id:2,created_at:stamp,before_at:stamp,before:[],after:[]},{id:1,created_at:stamp,before_at:stamp,before:[],after:[]}];
  const app = await appHarness({extra:{"/changes/history":p=>p.id ? versions.find(r=>r.id===p.id) : {revisions:versions}}});
  await app.click({action:"revisions-toggle"});
  versions=[{...versions[0],id:3},...versions];
  await app.click({action:"reload"});
  assert.match(app.element("#app").innerHTML,/<option value="3" selected>/);
  await app.listeners.change({target:{id:"revision-select",value:"1"}});
  versions=[{...versions[0],id:4},...versions];
  await app.click({action:"reload"});
  assert.match(app.element("#app").innerHTML,/<option value="1" selected>/);
  await app.click({action:"next"});
  assert.match(app.element("#app").innerHTML,/<option value="4" selected>/);
});

test("late history response cannot replace a newly selected week", async () => {
  const week=model.monday(model.today()),stamp=new Date().toISOString();
  let release;
  const app=await appHarness({extra:{"/changes/history":p=>{
    if(p.monday===week && !p.id)return new Promise(r=>{release=r});
    return p.id ? {id:2,created_at:stamp,before_at:stamp,before:[],after:[]} : {revisions:[{id:2,created_at:stamp,before_at:stamp}]};
  }}});
  const old=app.click({action:"revisions-toggle"});
  while(!release)await new Promise(r=>setTimeout(r,0));
  await app.click({action:"next"});
  release({revisions:[{id:1,created_at:stamp,before_at:stamp}]});await old;
  assert.match(app.element("#app").innerHTML,/<option value="2" selected>/);
  assert.doesNotMatch(app.element("#app").innerHTML,/<option value="1" selected>/);
});


test("revision cards retain bell numbers on both sides of a move and do not number by list position", () => {
  const grid = { times: [
    {id:42,number:1,minute_from:510,minute_to:605},
    {id:77,number:3,minute_from:740,minute_to:835},
  ]};
  const l = lesson(510,605,{id:1,time_id:42});
  const rows = revisionRows({before:[l],after:[{...l,time_id:77,minute_from:740,minute_to:835}]}, grid);
  assert.equal(rows.find(l=>l.revisionKind==="removed").number,1);
  assert.equal(rows.find(l=>l.revisionKind==="added").number,3);
  const oldBell = revisionRows({after:[{...l,minute_from:500,minute_to:600}]}, grid);
  assert.equal(oldBell[0].number,1); // Archived time, same bell slot.
  assert.equal(revisionRows({after:[{...l,time_id:0}]},grid)[0].number,1);
  assert.equal(revisionRows({after:[lesson(900,990)]},grid)[0].number,0); // Unknown slot stays unknown.
  assert.equal(revisionRows({after:[{...l,number:4}]},grid)[0].number,4);
});

test("schedule actions preserve scroll through short loading states and shorter days", async () => {
  const app = await appHarness();
  const node = app.element("#app");
  let html = node.innerHTML;
  const positions = [];
  Object.defineProperty(node, "innerHTML", {
    get: () => html,
    set: value => {
      html = value;
      // Browser behavior: replacing a tall page by short content immediately
      // clamps scrollY. The reserved height must exist before this happens.
      const documentHeight = Math.max(500, parseFloat(node.style.minHeight) || 0);
      app.viewport.scrollY = Math.min(app.viewport.scrollY, Math.max(0,documentHeight-app.viewport.innerHeight));
      positions.push(app.viewport.scrollY);
    },
  });
  app.viewport.scrollY=720;
  app.element(".schedule-scroll").scrollLeft=190;
  for (const action of [
    {action:"revisions-toggle"},
    {action:"revision-step",step:"1"},
    {action:"next"},
    {action:"prev"},
    {action:"view",view:"day"},
    {action:"day",date:model.shift(model.monday(model.today()),5)},
    {action:"revisions-toggle"},
  ]) {
    await app.click(action);
    assert.equal(app.viewport.scrollY,720,JSON.stringify(action));
  }
  assert.ok(positions.length>10); // Includes intermediate async renders.
  assert.ok(positions.every(y=>y===720));
  assert.equal(app.element(".schedule-scroll").scrollLeft,190);
  assert.match(html, /data-action="lesson"|Занятий на этот день нет/);
  app.viewport.scrollY=0;
  await app.click({action:"day",date:model.monday(model.today())});
  assert.equal(node.style.minHeight,""); // No permanent blank tail after returning to top.
});


test("initials survive repeated whitespace, NBSP and already abbreviated names", () => {
  for (const [input, expected] of [
    ["Артемьева  Е.С.", "Артемьева Е.С."],
    ["Артемьева\u00a0Е. С.", "Артемьева Е.С."],
    [" Артемьева  Елена Сергеевна ", "Артемьева Е.С."],
    ["Иванов Иван", "Иванов Иван"],
    ["Иванов Жан-Поль Сергеевич", "Иванов Ж.-П.С."],
    ["Иванов", "Иванов"],
  ]) assert.equal(ux.shortName(input), expected);
});

test("favorites and quick switches restore each group's subgroup including old preferences", async () => {
  const saved = memory();
  saved.setItem("demo.mp.preferences.v1", JSON.stringify({ group: 39, subgroup: 334,
    favorites: [{id:39,name:"Своя"}, {id:40,name:"Друга"}] }));
  const app = await appHarness({ saved });
  const reader = app.tools.get("read_schedule_week");
  await app.click({action:"favorite-open",id:"39"});
  assert.equal(new URLSearchParams(app.location.search).get("subgroup"), "334");
  await app.click({action:"favorite-open",id:"40"});
  await app.listeners.change({target:{id:"subgroup", value:"335"}});
  await app.click({action:"favorite-open",id:"39"});
  assert.equal(new URLSearchParams(app.location.search).get("subgroup"), "334");
  assert.ok(model.items(reader.execute({}).week).every(l => !l.subgroup_id || l.subgroup_id === 334));
  await app.click({action:"favorite-open",id:"40"});
  assert.equal(new URLSearchParams(app.location.search).get("subgroup"), "335");
  const restarted = await appHarness({ saved });
  await restarted.click({action:"home-group"});
  assert.equal(new URLSearchParams(restarted.location.search).get("group"), "39");
  assert.equal(new URLSearchParams(restarted.location.search).get("subgroup"), "334");
});

test("meeting duration and evening range survive links, reload and browser back", async () => {
  const app = await appHarness({hash:"#compare"});
  await app.click({action:"compare-mode", mode:"free"});
  await app.listeners.change({target:{id:"meeting-min",value:"90"}});
  await app.listeners.change({target:{id:"meeting-end",value:"1320"}});
  const query = app.location.search;
  assert.equal(new URLSearchParams(query).get("min"), "90");
  assert.equal(new URLSearchParams(query).get("until"), "1320");
  const reopened = await appHarness({query, hash:"#compare"});
  assert.match(reopened.element("#app").innerHTML, /value="90" selected/);
  assert.match(reopened.element("#app").innerHTML, /value="1320" selected/);
  assert.match(reopened.element("#app").innerHTML, /до 22:00/);
  const restored = await appHarness({saved:app.storage,hash:"#compare"});
  assert.match(restored.element("#app").innerHTML, /value="1320" selected/);
  restored.location.search = query.replace("min=90", "min=15");
  await restored.listeners["window:popstate"]();
  assert.match(restored.element("#app").innerHTML, /value="15" selected/);
});

test("parallel subgroup lessons share a row without absorbing later lessons or nonstudy time", () => {
  const a = lesson(510,605,{number:1,subgroup_id:334});
  const b = lesson(510,605,{id:2,number:1,subgroup_id:335});
  const c = lesson(625,720,{id:3,number:2});
  const rows = ux.groupedDayRows({items:[a,b,c]}, {});
  assert.equal(rows.length,2);
  assert.deepEqual(rows[0].lessons.map(l=>l.id), [1,2]);
  assert.equal(rows[1].lesson.id,3);
  assert.equal(ux.groupedDayRows({items:[a,{...b,flags:8}]},{}).length,2);
});

test("mirrored nearby matches are one opportunity with both pieces of evidence", () => {
  const a=lesson(510,605,{classroom:"к. 1/411"});
  const b=lesson(625,720,{id:2,classroom:"к. 1/412"});
  const grouped=groupedNearby(nearbyLessons([a,b],[a,b]));
  assert.equal(grouped.length,3);
  const handoff=grouped.find(m=>m.timing==="handoff");
  assert.equal(handoff.alternatives.length,2);
  assert.equal(grouped.filter(m=>m.sharedLesson).length,2);
});

test("revision summary explains a later start and changed room; topic-only edits stay silent", () => {
  const a=lesson(510,605,{classroom:"к. 1/411"});
  const b=lesson(625,720,{id:2,classroom:"к. 1/412"});
  const summary=revisionSummary({before:[a,b],after:[{...b,classroom:"к. 3/210"}]});
  assert.ok(summary.some(s=>s.includes("Начало дня теперь в 10:25")));
  assert.ok(summary.some(s=>s.includes("к. 1/412 → к. 3/210")));
  assert.deepEqual(revisionSummary({before:[a],after:[{...a,topic:"Новая тема"}]}),[]);
});

test("clock refresh updates the countdown without fetching data or replacing the app", async () => {
  let minute=600, requests=0;
  const app=await appHarness({clockMinute:()=>minute,transform:r=>{
    requests++;
    return {...r,week:{monday:model.monday(model.today()),days:[{date:model.today(),items:[lesson(590,610,{date:model.today(),discipline:"Пара сейчас"})]}]}};
  }});
  const original=app.element("#app").innerHTML, count=requests;
  minute=601;
  app.timers.find(t=>t.ms===60000).callback();
  assert.match(app.element(".focus-card").outerHTML,/Ещё 9 мин/);
  assert.equal(requests,count);
  assert.equal(app.element("#app").innerHTML,original);
});

test("cold offline startup restores a saved subgroup week without any network", async () => {
  const saved = memory();
  saved.setItem("mp.preferences.v1", JSON.stringify({ group: 39, subgroup: 334, view: "day" }));
  const seed = createData({ storage: saved, fetcher: async url => {
    const u = new URL(url, "http://localhost");
    const value = await createData({ demo: true }).request(u.pathname.slice(4), Object.fromEntries(u.searchParams));
    return { ok: true, json: async () => value };
  } });
  await seed.request("/schedule/week", { group: 39, subgroup: 334, monday: model.monday(model.today()) });
  await seed.request("/groups/subgroups", { group: 39 });
  // Exams deliberately absent: this optional request must not prevent startup.
  const app = await appHarness({ demo: false, saved, offline: true });
  const result = app.tools.get("read_schedule_week").execute({});
  assert.equal(result.group.id, 39);
  assert.equal(result.subgroup, 334);
  assert.equal(result.offline, true);
  assert.ok(result.week.days.some(d => d.items.length));
  assert.match(app.element("#app").innerHTML, /Сохранённая копия/);
  assert.doesNotMatch(app.element("#app").innerHTML, /Загружаем неделю/);
});

test("a stalled network cannot indefinitely block cached data, even when abort is ignored", async () => {
  const storage = memory();
  await createData({ storage, fetcher: async () => ({ ok: true, json: async () => ({ marker: "saved" }) }) }).request("/schedule/week", { group: 39 });
  for (const fetcher of [() => new Promise(() => {}), async () => ({ ok: true, json: () => new Promise(() => {}) })]) {
    const data = createData({ storage, fetcher, timeoutMs: 5 });
    const result = await data.request("/schedule/week", { group: 39 });
    assert.equal(result.marker, "saved");
    assert.equal(result.offline, true);
  }
  let requests = 0;
  const offline = createData({ storage, isOnline: () => false, fetcher: () => { requests++; throw Error("must not fetch"); } });
  assert.equal((await offline.request("/schedule/week", { group: 39 })).offline, true);
  await assert.rejects(offline.request("/schedule/week", { group: 40 }), /ещё не сохранена/);
  assert.equal(requests, 0);
});

test("teacher identities display full names, degree and departments and escape directory data", async () => {
  const dangerous = '<img src=x onerror="alert(1)">';
  const teacher = { id: 99, name: "Смирнов В.В.", full_name: "Смирнов Виктор Валерьевич", degree: "к.п.н.", departments: ["Кафедра медицины", dangerous] };
  const app = await appHarness({ extra: {
    "/teachers/search": { teachers: [teacher, { id: 100, name: "Смирнов В.В.", full_name: "Смирнов Владимир Владимирович" }] },
    "/teachers/week": { teacher, week: { days: [] }, profile: { subjects: [] } },
  } });
  await app.click({ route: "teachers" });
  let html = app.element("#app").innerHTML;
  assert.match(html, /Смирнов Виктор Валерьевич/);
  assert.match(html, /Смирнов Владимир Владимирович/);
  assert.match(html, /Кафедра медицины/);
  assert.match(html, /к.п.н./);
  assert.doesNotMatch(html, /<img/);
  await app.click({ action: "teacher", id: "99" });
  html = app.element("#app").innerHTML;
  assert.match(html, /<h1>Смирнов Виктор Валерьевич<\/h1>/);
  assert.match(html, /Кафедра медицины/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<img/);
});

test("my day keeps ongoing and unknown-time lessons, then crosses weeks", () => {
  const days = [
    { date: "2026-09-12", items: [lesson(600,690,{date:"2026-09-12"})] },
    { date: "2026-09-13", items: [lesson(600,690,{flags:8,date:"2026-09-13"})] },
    { date: "2026-09-14", items: [lesson(740,835,{date:"2026-09-14"})] },
  ];
  assert.equal(ux.studyDay(days,"2026-09-12",650).date,"2026-09-12");
  assert.equal(ux.studyDay(days,"2026-09-12",690).date,"2026-09-14");
  assert.equal(ux.studyDay(days,"2026-09-15",0),null);
  days[0].items.push(lesson(0,0,{id:3,date:"2026-09-12"}));
  assert.equal(ux.studyDay(days,"2026-09-12",1400).date,"2026-09-12");
  const summary=ux.dayOverview(days[0],demoGrid);
  assert.equal(summary.start,null);
  assert.equal(summary.end,null);
  assert.equal(summary.unknown,true);
});

test("my day distinguishes missed slots from breaks", () => {
  const day={items:[
    lesson(510,605,{classroom:"к. 1/210"}),
    lesson(740,835,{id:2,classroom:"к. 3/310"}),
    lesson(865,960,{id:3,classroom:"к. 3/312"}),
  ]};
  const result=ux.dayOverview(day,demoGrid);
  assert.equal(result.count,3);
  assert.equal(result.start,510);
  assert.equal(result.end,960);
  assert.deepEqual(result.windows,[{from:605,to:740}]);

});

test("opening day mode selects today, retains explicit shared dates and marks today while another day is selected", async () => {
  const date=model.today(), start=model.monday(date);
  const app=await appHarness({query:`?group=39&week=${start}`});
  await app.click({action:"view",view:"day"});
  assert.equal(new URLSearchParams(app.location.search).get("date"),date);
  assert.match(app.element("#app").innerHTML,new RegExp(`data-date="${date}" class="active today"[^>]*aria-current="date"`));
  const other=model.shift(date,date===start?1:-1);
  await app.click({action:"day",date:other});
  assert.match(app.element("#app").innerHTML,new RegExp(`data-date="${date}" class="today"[^>]*aria-pressed="false"`));
  const shared=await appHarness({query:`?group=39&date=${other}`});
  assert.equal(new URLSearchParams(shared.location.search).get("date"),other);
  await app.click({action:"view",view:"week"});
  await app.click({action:"view",view:"day"});
  assert.equal(new URLSearchParams(app.location.search).get("date"),date);
});

test("my day opens a future week and explains missing coverage and failed search", async () => {
  const next=model.shift(model.monday(model.today()),7);
  const app=await appHarness({extra:{"/schedule/upcoming":{
    from:model.today(),to:model.shift(model.today(),29),missing:true,
    days:[{date:next,missing:true,items:[lesson(625,720,{date:next})]}],grid:demoGrid,
  }}});
  assert.match(app.element("#app").innerHTML,/Расписание загружено частично/);
  assert.match(app.element("#app").innerHTML,new RegExp(`data-action="calendar"[^>]*data-date="${next}"`));
  await app.click({action:"calendar",date:next});
  assert.equal(new URLSearchParams(app.location.search).get("date"),next);
  assert.match(app.element("#app").innerHTML,/class="day-tabs"/);
  const empty=await appHarness({extra:{"/schedule/upcoming":{days:[],missing:true}}});
  assert.match(empty.element("#app").innerHTML,/Пока нельзя определить следующий учебный день/);
  const failed=await appHarness({transform:r=>({...r,week:{...r.week,days:[]}}),extra:{"/schedule/upcoming":()=>{throw new Error("offline");}}});
  assert.match(failed.element("#app").innerHTML,/Не удалось проверить следующие дни/);
  assert.doesNotMatch(failed.element("#app").innerHTML,/Не получилось загрузить/);
});


test("Sunday remains today even when the API omits a non-study day", async () => {
  const app=await appHarness({dateNow:()=>"2026-09-13",query:"?group=39&week=2026-09-07"});
  await app.click({action:"view",view:"day"});
  assert.equal(new URLSearchParams(app.location.search).get("date"),"2026-09-13");
  assert.match(app.element("#app").innerHTML,/data-date="2026-09-13" class="active today"/);
  assert.match(app.element("#app").innerHTML,/Занятий на этот день нет/);
});

test("daily summary rolls to the next study day at lesson end without another request", async () => {
  const date=model.today(), tomorrow=model.shift(date,1);
  let minute=600, requests=0;
  const app=await appHarness({clockMinute:()=>minute,extra:{"/schedule/upcoming":()=>{
    requests++;
    return {days:[{date,items:[lesson(590,610,{date})]},{date:tomorrow,items:[lesson(625,720,{date:tomorrow})]}],grid:demoGrid};
  }}});
  assert.match(app.element("#app").innerHTML,/Сегодня с 09:50/);
  minute=610;
  app.timers.find(t=>t.ms===60000).callback();
  assert.match(app.element(".my-day").outerHTML,/Завтра к 10:25/);
  assert.equal(requests,1);
});

test("cached upcoming days stay useful offline and do not claim fresh data", async () => {
  const date=model.today();
  const app=await appHarness({extra:{"/schedule/upcoming":{offline:true,stale:true,
    days:[{date:model.shift(date,8),items:[lesson(625,720,{date:model.shift(date,8)})]}],grid:demoGrid}}});
  assert.match(app.element("#app").innerHTML,/По сохранённому расписанию. Возможны изменения/);
  assert.match(app.element("#app").innerHTML,/Открыть день/);
});


test("daily composition groups class types, counts unknown times and ignores non-study slots", () => {
  const types=["Лекция", "лек", "Практика", "сем.", "Семинар", "Лб", "Экзамен", "", "<img src=x>"];
  const day={items:types.map((class_type,id)=>lesson(0,0,{id,class_type}))};
  day.items.push(lesson(0,0,{id:99,class_type:"Лекция",flags:8}));
  const summary=ux.dayOverview(day,demoGrid);
  assert.equal(summary.count,9);
  assert.deepEqual(summary.kinds.map(({label,count})=>[label,count]),[
    ["Лекции",2],["Практические",1],["Семинары",2],["Лабораторные",1],["Экзамен",1],["Тип не указан",1],["<img src=x>",1],
  ]);
});

test("next day shows its composition without expanding details or suggesting building transfers", async () => {
  const date=model.shift(model.today(),1);
  const app=await appHarness({extra:{"/schedule/upcoming":{days:[{date,items:[
    lesson(510,605,{date,class_type:"Лекция",classroom:"к. 1/210"}),
    lesson(625,720,{id:2,date,class_type:"Семинар",classroom:"к. 3/210"}),
    lesson(740,835,{id:3,date,class_type:"<img src=x>",classroom:"к. 1/210"}),
  ]}],grid:demoGrid}}});
  const card=app.element("#app").innerHTML.match(/<section class="my-day"[\s\S]*?<\/section>/)[0];
  assert.match(card,/<ul class="my-day-kinds" aria-label="Состав дня">/);
  assert.match(card,/<li>1 лекция<\/li>/);
  assert.match(card,/<li>1 семинар<\/li>/);
  assert.match(card,/&lt;img src=x&gt;/);
  assert.doesNotMatch(card,/смен.*корпус|перерыв|<img|<details/);
});

test("installation mark and university render safely in desktop and mobile navigation", async () => {
  const app = await appHarness({ profile: { app_name: "Учебный портал", university: "Другой <вуз>", web_mark: "<У>" } });
  const html = app.element("#app").innerHTML;
  assert.equal((html.match(/class="brand-symbol">&lt;У&gt;/g) || []).length, 2);
  assert.match(html, /class="breadcrumb">Другой &lt;вуз&gt;/);
  assert.doesNotMatch(html, /class="brand-symbol">м\./);
});

const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

test("identical in-flight reads share one fetch, then allow a fresh refresh", async () => {
  const gate = deferred();
  let calls = 0, reads = 0;
  const saved = memory();
  const storage = { ...saved, getItem: key => { reads++; return saved.getItem(key); } };
  const data = createData({ storage, fetcher: async () => {
    calls++;
    await gate.promise;
    return { ok: true, json: async () => ({ version: calls }) };
  } });
  const first = data.request("/schedule/week", { group: 39, subgroup: 0 });
  const second = data.request("/schedule/week", { subgroup: 0, group: 39 });
  assert.equal(calls, 1);
  assert.equal(reads, 0, "do not deserialize the offline cache before a successful fetch");
  gate.resolve();
  assert.deepEqual(await first, await second);
  assert.equal(reads, 1, "merge the cache once for the shared response");
  assert.equal((await data.request("/schedule/week", { group: 39, subgroup: 0 })).version, 2);
});

test("failed shared requests are evicted and different subgroups stay independent", async () => {
  const gate = deferred();
  let calls = 0;
  const data = createData({ fetcher: async () => {
    calls++;
    await gate.promise;
    return { ok: false, status: 404 };
  } });
  const requests = [
    data.request("/schedule/week", { group: 39, subgroup: 1 }),
    data.request("/schedule/week", { group: 39, subgroup: 1 }),
    data.request("/schedule/week", { group: 39, subgroup: 2 }),
  ];
  assert.equal(calls, 2);
  gate.resolve();
  assert.ok((await Promise.allSettled(requests)).every(r => r.status === "rejected" && r.reason.status === 404));
  await assert.rejects(data.request("/schedule/week", { group: 39, subgroup: 1 }));
  assert.equal(calls, 3);
});

test("both comparison groups start loading before either week returns", async () => {
  const gate = deferred(), started = [];
  const slow = await appHarness({ hash: "#settings", extra: {
    "/schedule/week": async params => {
      started.push(params.group);
      await gate.promise;
      return createData({ demo: true }).request("/schedule/week", params);
    },
  } });
  const loading = slow.click({ route: "compare" });
  await new Promise(resolve => setImmediate(resolve));
  try { assert.equal(new Set(started).size, 2); }
  finally { gate.resolve(); await loading; }
  assert.match(slow.element("#app").innerHTML, /proximity-card/);
});

test("typing while departments load retains both search results and institute choices", async () => {
  const departments = deferred();
  const app = await appHarness({ extra: { "/groups/departments": () => departments.promise } });
  const opening = app.click({ action: "pick", target: "group" });
  await new Promise(resolve => setImmediate(resolve));
  app.listeners.input({ target: { id: "group-search", value: "леч" } });
  await new Promise(resolve => setTimeout(resolve, 300));
  const results = app.element("#group-results").innerHTML;
  departments.resolve({ departments: [{ id: 1, name: "Новый институт" }] });
  await opening;
  assert.match(app.element("#department").innerHTML, /Новый институт/);
  assert.equal(app.element("#group-results").innerHTML, results);
});

test("changing institute cancels pending text search and clearing course removes old results", async () => {
  let searches = 0;
  const app = await appHarness({ extra: { "/groups/search": () => { searches++; return { groups: [] }; } } });
  await app.click({ action: "pick", target: "group" });
  const before = searches;
  app.listeners.input({ target: { id: "group-search", value: "леч" } });
  await app.listeners.change({ target: { id: "department", value: "1" } });
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal(searches, before);
  assert.match(app.element("#group-results").innerHTML, /Выберите курс/);
  await app.listeners.change({ target: { id: "course", value: "2" } });
  await app.listeners.change({ target: { id: "course", value: "" } });
  assert.doesNotMatch(app.element("#group-results").innerHTML, /pick-option/);
  assert.match(app.element("#group-results").innerHTML, /Выберите курс/);
});

test("a failed catalog from a closed picker cannot overwrite the reopened picker", async () => {
  const old = deferred();
  let calls = 0;
  const app = await appHarness({ extra: { "/groups/departments": () => ++calls === 1 ? old.promise : { departments: [] } } });
  const opening = app.click({ action: "pick", target: "group" });
  await new Promise(resolve => setImmediate(resolve));
  await app.click({ action: "close-picker" });
  await app.click({ action: "pick", target: "group" });
  const results = app.element("#group-results").innerHTML;
  old.reject(new Error("old request failed"));
  await opening;
  assert.equal(app.element("#group-results").innerHTML, results);
});

test("shared offline reads find existing cache entries regardless of parameter order", async () => {
  const storage = memory();
  storage.setItem("mp.schedule-cache.v1", JSON.stringify({
    "/api/schedule/week?group=39&subgroup=1": { value: { group: { id: 39 } }, saved: "2026-09-13T10:00:00Z" },
  }));
  const data = createData({ storage, isOnline: () => false });
  const [first, second] = await Promise.all([
    data.request("/schedule/week", { subgroup: 1, group: 39 }),
    data.request("/schedule/week", { group: 39, subgroup: 1 }),
  ]);
  assert.deepEqual(first, second);
  assert.equal(first.offline, true);
  assert.equal(first.stale, true);
  assert.equal(first.group.id, 39);
});

test("upcoming preview opens the right date when another week reuses lesson IDs", async () => {
  const date = "2026-09-14";
  const app = await appHarness({ dateNow: () => "2026-09-13", query: "?group=39&week=2026-09-07",
    transform: r => r.week.monday === "2026-09-07" ? ({ ...r, week: { ...r.week, days: [{date: "2026-09-07", items: [lesson(510,605,{discipline:"Прошлая неделя"})]}] } }) : r,
    extra: { "/schedule/upcoming": {days: [{date, items: [
      lesson(625,720,{date,discipline:"Следующая неделя",class_type:"Лабораторная"}),
      lesson(740,835,{id:2,date,class_type:"Лабораторная"}),
    ]}], grid: demoGrid} },
  });
  const card = app.element("#app").innerHTML.match(/<section class="my-day"[\s\S]*?<\/section>/)[0];
  assert.match(card, /2 лабораторные/);
  assert.equal((card.match(/data-action="upcoming-lesson"/g) || []).length, 2);
  await app.click({action:"upcoming-lesson", id:"1", date});
  assert.equal(app.element("#detail").open, true);
  assert.match(app.element("#detail").innerHTML, /Следующая неделя/);
  assert.match(app.element("#detail").innerHTML, /14 сентября/);
  assert.doesNotMatch(app.element("#detail").innerHTML, /Прошлая неделя/);
  await app.click({action:"close-detail"});
  await app.click({action:"calendar",date});
  assert.equal(new URLSearchParams(app.location.search).get("date"),date);
});

test("today's preview skips ended lessons, retains unknown times and explains its limit", async () => {
  const date = "2026-09-14";
  const app = await appHarness({dateNow: () => date, clockMinute: () => 650,
    extra: {"/schedule/upcoming": {days: [{date, items: [
      lesson(510,605,{id:1,date,discipline:"Уже закончилась"}),
      lesson(625,720,{id:2,date,discipline:"Сейчас идёт"}),
      lesson(740,835,{id:3,date}), lesson(865,960,{id:4,date}),
      lesson(980,1075,{id:5,date}), lesson(0,0,{id:6,date,discipline:"Без времени"}),
    ]}], grid: demoGrid}},
  });
  const card = app.element("#app").innerHTML.match(/<section class="my-day"[\s\S]*?<\/section>/)[0];
  assert.doesNotMatch(card, /Уже закончилась/);
  assert.doesNotMatch(card, /<strong>Без времени<\/strong>/);
  assert.match(card, /Сейчас идёт/);
  assert.match(card, /my-day-status">Сейчас/);
  assert.equal((card.match(/data-action="upcoming-lesson"/g)||[]).length, 3);
  assert.match(card, /Первые 3 из 5/);
  assert.match(card, /Ещё 2 занятия · открыть день/);
  assert.match(card, /У части занятий нет времени/);
});

test("calendar tracks the selected day across a month boundary and distinguishes today", async () => {
  const app = await appHarness({dateNow: () => "2026-09-30", query:"?group=39&week=2026-09-28"});
  await app.click({action:"calendar",date:"2026-10-01"});
  const calendar = app.element("#app").innerHTML.match(/<section class="panel mini-calendar"[\s\S]*?<\/section>/)[0];
  assert.match(calendar, /Октябрь 2026/);
  assert.match(calendar, /class="selected active"[^>]*data-date="2026-10-01"[^>]*aria-pressed="true"/);
  assert.match(calendar, /class="outside-month selected today"[^>]*data-date="2026-09-30"[^>]*aria-current="date"/);
  assert.match(calendar, /class="outside-month"[^>]*data-date="2026-11-01"/);
});
