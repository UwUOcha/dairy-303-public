import { snapshot, diffLessons } from "./changes.mjs";

// A git-style patch keeps unchanged lessons and both sides of an edit. Removed
// lessons keep their original day/time, including moves outside the current day.
export function revisionRows(revision, grid) {
  // Number by the bell slot, never by position among lessons in this snapshot.
  // Keep each side's metadata separately so a moved lesson has both numbers.
  const numbered = (rows) => {
    const source = new Map((rows || []).map((l) => [l.id, l]));
    return snapshot({ days: [{ items: rows || [] }] }).map((l) => {
      const original = source.get(l.id);
      const slot =
        (grid?.times || []).find(
          (t) => original.time_id > 0 && t.id === original.time_id,
        ) ||
        (grid?.times || []).find(
          (t) =>
            l.minute_to > l.minute_from &&
            t.minute_from === l.minute_from &&
            t.minute_to === l.minute_to,
        );
      const number = original.number > 0 ? original.number : slot?.number || 0;
      return { ...l, number };
    });
  };
  const before = numbered(revision.before),
    after = numbered(revision.after);
  // Topics entered after a class aren't a schedule revision.
  for (const l of [...before, ...after]) delete l.topic;
  const changes = diffLessons(before, after),
    added = new Set(changes.filter((c) => c.after).map((c) => c.after.id));
  return [
    ...after.map((l) => ({
      ...l,
      revisionKind: added.has(l.id) ? "added" : "unchanged",
    })),
    ...changes
      .filter((c) => c.before)
      .map((c) => ({ ...c.before, revisionKind: "removed" })),
  ].sort(
    (a, b) =>
      a.date.localeCompare(b.date) ||
      a.minute_from - b.minute_from ||
      a.id - b.id ||
      (a.revisionKind === "removed" ? -1 : 1),
  );
}
