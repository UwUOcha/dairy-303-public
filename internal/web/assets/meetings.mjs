import { actual, freeTogether } from "./model.mjs";

// Only recognized building/room notation is interpreted. Bare "254" does
// not identify a building; addresses and ambiguous ranges need a directory.
export function parseClassroom(value) {
  const text = String(value || "")
    .trim()
    .toLocaleLowerCase("ru");
  if (!text || /онлайн|дистанц|zoom|teams|https?:/.test(text)) return null;
  if (
    [...text.matchAll(/(?:корпус|корп\.?|к\.)\s*\d+/gu)].length > 1 ||
    /[,;]\s*\d+[а-яa-z]?\s*\//u.test(text)
  )
    return null;
  const explicit = text.match(
    /(?:^|[\s,(])(?:корпус|корп\.?|к\.)\s*(\d+[а-яa-z]?)(?=$|[\s/,;)])/u,
  );
  const shorthand = text.match(
    /^(\d+[а-яa-z]?)\s*\/\s*(\d+[а-яa-z]?)(?=$|[\s,;)])/u,
  );
  if (!explicit && !shorthand) return null;
  const building = (explicit?.[1] || shorthand[1]).replace(/^0+(?=\d)/, "");
  const rest = explicit ? text.slice(explicit.index + explicit[0].length) : "";
  const roomMatch = explicit
    ? rest.match(
        /^(?:\s*\/\s*|\s*[,;]?\s*ауд(?:итория)?\.?\s*)(\d+[а-яa-z]?)(?=$|[\s,;)])/u,
      )
    : shorthand;
  const room = (explicit ? roomMatch?.[1] : roomMatch?.[2]) || "";
  const floorMatch = text.match(
    /(?:^|[\s,(])(?:этаж\s*(\d{1,2})|(\d{1,2})(?:-?й)?\s*этаж)(?=$|[\s,;)])/u,
  );
  const explicitFloor = floorMatch
    ? Number(floorMatch[1] || floorMatch[2])
    : null;
  // 4-digit room numbers and letter prefixes have no safe generic floor rule.
  const inferred = /^[1-9]\d{2}[а-яa-z]?$/u.test(room) ? Number(room[0]) : null;
  return {
    building,
    room,
    floor: explicitFloor ?? inferred,
    floorInferred: explicitFloor === null && inferred !== null,
  };
}

const onsite = (l) =>
  actual(l) &&
  !((l.flags || 0) & 4) &&
  l.minute_to > l.minute_from &&
  parseClassroom(l.classroom);

export function nearbyLessons(left, right) {
  const out = [],
    seen = new Set();
  for (const a of left)
    for (const b of right) {
      const va = onsite(a),
        vb = onsite(b);
      if (!va || !vb || a.date !== b.date || va.building !== vb.building)
        continue;
      const overlapFrom = Math.max(a.minute_from, b.minute_from),
        overlapTo = Math.min(a.minute_to, b.minute_to);
      // Also show adjacent lessons: one group leaves while the other arrives.
      // Neither case is a promise of shared free time.
      if (overlapFrom - overlapTo > 30) continue;
      const timing = overlapTo > overlapFrom ? "overlap" : "handoff";
      const from = Math.min(overlapFrom, overlapTo),
        to = Math.max(overlapFrom, overlapTo);
      const sameRoom = va.room && va.room === vb.room;
      const sameFloor = va.floor !== null && va.floor === vb.floor;
      const kind = sameRoom ? "room" : sameFloor ? "floor" : "building";
      const key = `${a.date}:${a.id}:${b.id}:${a.classroom}:${b.classroom}:${from}:${to}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({
        a,
        b,
        from,
        to,
        building: va.building,
        kind,
        floor: sameFloor ? va.floor : null,
        floorInferred: sameFloor && (va.floorInferred || vb.floorInferred),
        sharedLesson: a.id === b.id,
        timing,
      });
    }
  const rank = { room: 0, floor: 1, building: 2 };
  return out.sort(
    (a, b) => a.from - b.from || rank[a.kind] - rank[b.kind] || a.to - b.to,
  );
}

// A short shared break is suggested only when both groups have just finished
// on-site classes in the same building. Clip it to 30 minutes after the earlier
// ending: beyond that, the schedule no longer says where those people might be.
export function nearbyBreaks(left, right) {
  if (
    [...left, ...right].some((l) => actual(l) && l.minute_to <= l.minute_from)
  )
    return [];
  const out = new Map(),
    rank = { room: 0, floor: 1, building: 2 };
  for (const match of nearbyLessons(left, right)) {
    if (match.timing !== "overlap") continue;
    const from = Math.max(match.a.minute_to, match.b.minute_to);
    const to = Math.min(match.a.minute_to, match.b.minute_to) + 30;
    if (to - from < 5) continue;
    // The immediate break must be unoccupied for BOTH selected subgroups.
    const slot = freeTogether(left, right, { from, to, min: 5 }).find(
      ([start]) => start === from,
    );
    if (!slot) continue;
    const key = `${match.a.date}:${match.building}:${slot[0]}:${slot[1]}`;
    const previous = out.get(key);
    if (!previous || rank[match.kind] < rank[previous.kind])
      out.set(key, { ...match, from: slot[0], to: slot[1] });
  }
  return [...out.values()].sort(
    (a, b) => a.from - b.from || rank[a.kind] - rank[b.kind],
  );
}
