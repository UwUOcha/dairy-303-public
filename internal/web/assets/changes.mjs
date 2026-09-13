import { actual } from "./model.mjs";

// Legacy exports stay available to older cached PWA shells during an update.
// The current application uses only normalization and diff; its history is server-side.
export const HISTORY_KEY = "mp.changes.v1";
const fields = [
  "date",
  "minute_from",
  "minute_to",
  "discipline",
  "class_type",
  "classroom",
  "staff",
  "subgroup_id",
  "audience_label",
  "flags",
  "comments",
  "topic",
];
const clean = (s) =>
  String(s || "")
    .trim()
    .replace(/\s+/g, " ");

export function snapshot(week) {
  if (!Array.isArray(week?.days)) throw new Error("Invalid schedule");
  const lessons = week.days
    .flatMap((d) => d.items || [])
    .filter(actual)
    .map((l) => {
      if (
        !Number.isSafeInteger(l.id) ||
        l.id <= 0 ||
        !/^\d{4}-\d{2}-\d{2}$/.test(l.date) ||
        !Number.isFinite(l.minute_from) ||
        !Number.isFinite(l.minute_to)
      )
        throw new Error("Invalid lesson");
      return {
        id: l.id,
        date: l.date,
        minute_from: l.minute_from,
        minute_to: l.minute_to,
        discipline: clean(l.discipline),
        class_type: clean(l.class_type),
        classroom: clean(l.classroom),
        staff: [...new Set((l.staff || []).map(clean))].sort(),
        subgroup_id: l.subgroup_id || 0,
        audience_label: clean(l.audience_label),
        flags: l.flags || 0,
        comments: clean(l.comments),
        topic: clean(l.topic),
      };
    });
  if (lessons.length > 300) throw new Error("Schedule too large");
  return lessons.sort(
    (a, b) =>
      a.date.localeCompare(b.date) ||
      a.minute_from - b.minute_from ||
      a.id - b.id,
  );
}
const signature = (l) => JSON.stringify(fields.map((f) => l[f]));
export function diffLessons(before, after) {
  const old = new Map(before.map((l) => [l.id, l])),
    fresh = new Map(after.map((l) => [l.id, l])),
    result = [];
  for (const [id, a] of old) {
    const b = fresh.get(id);
    if (!b) continue;
    old.delete(id);
    fresh.delete(id);
    const changed = fields.filter(
      (f) => JSON.stringify(a[f]) !== JSON.stringify(b[f]),
    );
    if (changed.length)
      result.push({
        kind: changed.some((f) =>
          ["date", "minute_from", "minute_to"].includes(f),
        )
          ? "moved"
          : "updated",
        before: a,
        after: b,
        fields: changed,
      });
  }
  // Upstream can recreate IDs without changing what students see. Match those
  // records exactly; never guess whether similar subjects are the same class.
  for (const [id, a] of old) {
    const equivalent = [...fresh].find(
      ([, b]) => signature(a) === signature(b),
    );
    if (equivalent) {
      old.delete(id);
      fresh.delete(equivalent[0]);
    }
  }
  for (const a of old.values())
    result.push({ kind: "removed", before: a, after: null, fields: [] });
  for (const b of fresh.values())
    result.push({ kind: "added", before: null, after: b, fields: [] });
  return result.sort((a, b) => {
    const x = a.after || a.before,
      y = b.after || b.before;
    return (
      x.date.localeCompare(y.date) ||
      x.minute_from - y.minute_from ||
      x.id - y.id
    );
  });
}
const token = (lessons) => JSON.stringify(lessons);
function validRecord(record) {
  try {
    return (
      typeof record?.usedAt === "string" &&
      typeof record?.base?.at === "string" &&
      typeof record?.latest?.at === "string" &&
      Number.isFinite(Date.parse(record.base.at)) &&
      Number.isFinite(Date.parse(record.latest.at)) &&
      Number.isFinite(Date.parse(record.usedAt)) &&
      JSON.stringify(snapshot({ days: [{ items: record.base.lessons }] })) ===
        JSON.stringify(record.base.lessons) &&
      JSON.stringify(snapshot({ days: [{ items: record.latest.lessons }] })) ===
        JSON.stringify(record.latest.lessons)
    );
  } catch {
    return false;
  }
}

export function createChangeHistory(
  storage,
  { key = HISTORY_KEY, now = () => new Date().toISOString() } = {},
) {
  let memory = {},
    durable = true;
  function read() {
    if (!durable) return { ...memory };
    try {
      const raw = storage?.getItem(key);
      if (raw) {
        const value = JSON.parse(raw);
        if (value && typeof value === "object" && !Array.isArray(value))
          memory = value;
      } else memory = {};
    } catch {}
    return { ...memory };
  }
  function save(records) {
    memory = Object.fromEntries(
      Object.entries(records)
        .filter(([, v]) => validRecord(v))
        .sort((a, b) => b[1].usedAt.localeCompare(a[1].usedAt))
        .slice(0, 24),
    );
    try {
      const raw = JSON.stringify(memory);
      storage.setItem(key, raw);
      durable = storage.getItem(key) === raw;
    } catch {
      durable = false;
    }
  }
  function report(id, record, first = false) {
    return {
      key: id,
      status: first ? "first" : "ready",
      durable,
      since: record.base.at,
      checkedAt: record.latest.at,
      token: token(record.latest.lessons),
      changes: diffLessons(record.base.lessons, record.latest.lessons),
    };
  }
  return {
    observe(response, subgroup = 0) {
      // A successful HTTP response may still be incomplete or stale.
      if (
        !response?.week ||
        !Number.isSafeInteger(response?.group?.id) ||
        response.missing ||
        response.stale ||
        response.offline
      )
        return { status: "unavailable", changes: [] };
      const id = `${response.group.id}:${subgroup}:${response.week.monday}`,
        records = read(),
        at = now();
      let lessons;
      try {
        lessons = snapshot(response.week);
      } catch {
        return { status: "unavailable", changes: [] };
      }
      let record = records[id],
        first = !validRecord(record);
      if (first)
        record = { base: { lessons, at }, latest: { lessons, at }, usedAt: at };
      else {
        record = { ...record, latest: { lessons, at }, usedAt: at };
        // Refresh the baseline only when nothing is pending. Pending changes
        // survive reloads and subsequent updates until explicitly acknowledged.
        if (!diffLessons(record.base.lessons, lessons).length)
          record.base = { lessons, at };
      }
      records[id] = record;
      save(records);
      return report(id, record, first);
    },
    acknowledge(id, expectedToken) {
      const records = read(),
        record = records[id];
      if (
        !validRecord(record) ||
        token(record.latest.lessons) !== expectedToken
      )
        return false;
      record.base = { ...record.latest };
      record.usedAt = now();
      save(records);
      return true;
    },
    clear() {
      memory = {};
      try {
        storage.removeItem(key);
      } catch {}
    },
  };
}

export function exampleChanges(week) {
  const before = snapshot(week),
    after = structuredClone(before);
  if (after[0]) {
    after[0].minute_from += 20;
    after[0].minute_to += 20;
  }
  if (after[1]) after[1].classroom = "к. 2/315";
  if (after[2]) after.splice(2, 1);
  if (before[3])
    after.push({
      ...before[3],
      id: 999999999,
      discipline: "Консультация",
      minute_from: 1080,
      minute_to: 1125,
    });
  return { status: "ready", demo: true, changes: diffLessons(before, after) };
}
