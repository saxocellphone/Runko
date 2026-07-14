// Agent-activity kind presentation + filtering (§12.6.1, decided
// 2026-07-14). The kind vocabulary is deliberately soft server-side
// (unknown coerces to "note" at ingest), so everything here normalizes
// the same way - render defensively, never reject. The glyph map realizes
// the spec's "now: ✎ path" presence line; the filter is client-side only
// (the card already over-fetches and refetches wholesale on pokes).

export const ACTIVITY_KINDS = ["read", "edit", "command", "search", "note"] as const;
export type ActivityKind = (typeof ACTIVITY_KINDS)[number];

export const kindMeta: Record<ActivityKind, { glyph: string; label: string }> = {
  read: { glyph: "≡", label: "read" },
  edit: { glyph: "✎", label: "edit" },
  command: { glyph: "❯", label: "command" },
  search: { glyph: "⌕", label: "search" },
  note: { glyph: "✱", label: "note" },
};

export function normalizeKind(kind: string): ActivityKind {
  return (ACTIVITY_KINDS as readonly string[]).includes(kind) ? (kind as ActivityKind) : "note";
}

export function countByKind(events: { kind: string }[]): Record<ActivityKind, number> {
  const counts: Record<ActivityKind, number> = { read: 0, edit: 0, command: 0, search: 0, note: 0 };
  for (const ev of events) counts[normalizeKind(ev.kind)] += 1;
  return counts;
}

// parseStoredKinds reads the persisted chip selection back. A first visit
// (null) or anything unparseable/non-array falls back to all-visible -
// hiding data by default is worse than noise. A stored valid selection is
// respected verbatim, including [] (the user hid everything on purpose);
// entries outside the vocabulary are dropped.
export function parseStoredKinds(raw: string | null): ActivityKind[] {
  if (raw === null) return [...ACTIVITY_KINDS];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [...ACTIVITY_KINDS];
    return ACTIVITY_KINDS.filter((k) => parsed.includes(k));
  } catch {
    return [...ACTIVITY_KINDS];
  }
}
