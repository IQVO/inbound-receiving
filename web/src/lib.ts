import { NO_VALUE } from "@warehouse/ui-kit";
import type { Discrepancy } from "./types";

/**
 * A strictly whole, non-negative number typed into a field, or null when
 * the text is empty or not a whole number ("12.5", "1e3", "abc", "-1").
 */
export function wholeNumber(raw: string): number | null {
  const s = raw.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) ? n : null;
}

/**
 * A `datetime-local` value ("2026-10-09T08:00" or with seconds) read as UTC
 * and written as RFC 3339 ("2026-10-09T08:00:00Z"); null when not a valid
 * instant. Pickers are labelled "(UTC)", so the operator's browser time
 * zone never shifts the instant that is sent.
 */
export function utcInputToIso(value: string): string | null {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value.trim());
  if (!m) return null;
  const iso = `${m[1]}T${m[2]}:${m[3]}:${m[4] ?? "00"}Z`;
  return Number.isNaN(Date.parse(iso)) ? null : iso;
}

/** "2026-10-09 08:05Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 16)}Z`;
}

/** "08:00" (UTC) for an RFC 3339 instant. */
export function utcTime(iso: string): string {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? iso : new Date(t).toISOString().slice(11, 16);
}

/** Today's date in UTC as "YYYY-MM-DD" (the board's default day). */
export function todayUtc(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/** "YYYY-MM-DD" -> [from, to) as RFC 3339 UTC instants spanning that day; null when not a real date. */
export function dayRange(day: string): { from: string; to: string } | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return null;
  const start = Date.parse(`${day}T00:00:00Z`);
  if (Number.isNaN(start) || new Date(start).toISOString().slice(0, 10) !== day) return null;
  const end = new Date(start + 24 * 60 * 60 * 1000);
  return { from: `${day}T00:00:00Z`, to: end.toISOString().replace(".000Z", "Z") };
}

/** SKU rule of the API: 1..64 characters, no whitespace, control characters or "/". */
export function skuProblem(sku: string): string | null {
  if (sku.length === 0) return "Enter a SKU.";
  if (sku.length > 64) return "A SKU is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(sku)) return "A SKU cannot contain spaces, control characters or '/'.";
  return null;
}

/** ASN-number rule of the API: 1..64 characters of [A-Za-z0-9._-]. */
export function asnNumberProblem(asnNumber: string): string | null {
  if (asnNumber.length === 0) return "Enter an ASN number.";
  if (asnNumber.length > 64) return "An ASN number is at most 64 characters.";
  if (!/^[A-Za-z0-9._-]+$/.test(asnNumber)) return "An ASN number may only contain letters, digits, '.', '_' and '-'.";
  return null;
}

/** Door-code rule of the API: 1..64 characters, no whitespace, control characters or "/". */
export function doorCodeProblem(door: string): string | null {
  if (door.length === 0) return "Choose or enter a dock door.";
  if (door.length > 64) return "A door code is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(door)) return "A door code cannot contain spaces, control characters or '/'.";
  return null;
}

/** "ASN-1, ASN-2  ASN-3" -> ["ASN-1", "ASN-2", "ASN-3"] (commas and whitespace separate). */
export function parseAsnNumbers(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

export const MAX_WINDOW_MS = 4 * 60 * 60 * 1000;

/** Units still expected on a receipt line (never negative; over-receipt shows as 0 remaining). */
export function remainingQty(expected: number, good: number, damaged: number): number {
  return Math.max(0, expected - good - damaged);
}

/** "1 Short · 1 Over · 1 Damaged" for the discrepancies (one count per kind), or null when there are none. */
export function summarize(discrepancies: readonly Discrepancy[]): string | null {
  if (discrepancies.length === 0) return null;
  const counts = new Map<string, number>();
  for (const d of discrepancies) counts.set(d.kind, (counts.get(d.kind) ?? 0) + 1);
  return ["Short", "Over", "Damaged"]
    .filter((k) => counts.has(k))
    .map((k) => `${counts.get(k)} ${k}`)
    .join(" · ");
}
