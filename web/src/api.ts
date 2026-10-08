import { INBOUND_RECEIVING_API_BASE } from "./config";
import type {
  Appointment,
  AppointmentPage,
  Asn,
  AsnPage,
  BookAppointmentInput,
  DockList,
  ListAppointmentsQuery,
  ListAsnsQuery,
  ListReceiptsQuery,
  OpenReceiptInput,
  ReceiveLineInput,
  Receipt,
  ReceiptPage,
  RegisterAsnInput,
} from "./types";

/** RFC 7807 problem+json body every error response from inbound-receiving
 *  returns (`type` = https://errors.inbound-receiving.warehouse-systems.dev/<slug>). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** Last path segment of problem.type (e.g. "door-window-overlap"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

/**
 * A fresh RFC 4122 v4 UUID for an Idempotency-Key header: minted once per
 * POST. `crypto.randomUUID` only exists in secure contexts, and the console
 * can be opened over plain http on a non-localhost host, so fall back to
 * getRandomValues.
 */
export function newIdempotencyKey(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

type Query = Record<string, string | number | boolean | undefined>;

function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

export interface ApiResult<T> {
  status: number;
  data: T;
}

/**
 * Every POST (the four resource-creating ones, which REQUIRE the key, and the
 * cancel / check-in / close actions, which accept it) carries its own
 * `Idempotency-Key`, a UUID minted for that request. GETs send no custom
 * header, so they stay simple CORS requests.
 */
async function request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<ApiResult<T>> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method === "POST") headers["Idempotency-Key"] = newIdempotencyKey();
  let res: Response;
  try {
    res = await fetch(`${INBOUND_RECEIVING_API_BASE}${path}`, {
      method,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return { status: res.status, data: (await res.json()) as T };
}

const seg = encodeURIComponent;

/** GET /asns?limit=&cursor=&state= */
export async function listAsns(q: ListAsnsQuery): Promise<AsnPage> {
  const res = await request<AsnPage>("GET", withQuery("/asns", { limit: q.limit, cursor: q.cursor, state: q.state }));
  return { items: res.data.items ?? [], nextCursor: res.data.nextCursor || undefined };
}

/** GET /asns/{asnNumber} */
export async function getAsn(asnNumber: string): Promise<Asn> {
  return (await request<Asn>("GET", `/asns/${seg(asnNumber)}`)).data;
}

/** POST /asns -> 201 (409 asn-already-exists, 422 unknown-sku). */
export function registerAsn(input: RegisterAsnInput): Promise<ApiResult<Asn>> {
  return request<Asn>("POST", "/asns", input);
}

/** POST /asns/{asnNumber}/cancel (409 asn-in-progress / asn-terminal). */
export async function cancelAsn(asnNumber: string, reason?: string): Promise<Asn> {
  const body = reason ? { reason } : undefined;
  return (await request<Asn>("POST", `/asns/${seg(asnNumber)}/cancel`, body)).data;
}

/** GET /appointments?door=&state=&from=&to=&limit=&cursor= */
export async function listAppointments(q: ListAppointmentsQuery): Promise<AppointmentPage> {
  const res = await request<AppointmentPage>(
    "GET",
    withQuery("/appointments", {
      limit: q.limit,
      cursor: q.cursor,
      door: q.door,
      state: q.state,
      from: q.from,
      to: q.to,
    }),
  );
  return { items: res.data.items ?? [], nextCursor: res.data.nextCursor || undefined };
}

const MAX_PAGES = 20;

/** Every appointment matching `q`, following nextCursor to the last page (capped at 20 pages of 500). */
export async function listAllAppointments(q: Omit<ListAppointmentsQuery, "cursor" | "limit">): Promise<Appointment[]> {
  const all: Appointment[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const res = await listAppointments({ ...q, limit: 500, cursor });
    all.push(...res.items);
    if (!res.nextCursor) break;
    cursor = res.nextCursor;
  }
  return all;
}

/** POST /appointments -> 201 (409 door-window-overlap / asn-not-receivable, 422 unknown-asn / unknown-dock-door). */
export function bookAppointment(input: BookAppointmentInput): Promise<ApiResult<Appointment>> {
  return request<Appointment>("POST", "/appointments", input);
}

/** POST /appointments/{id}/check-in (409 appointment-not-booked / outside-check-in-window). */
export async function checkInAppointment(appointmentId: string): Promise<Appointment> {
  return (await request<Appointment>("POST", `/appointments/${seg(appointmentId)}/check-in`)).data;
}

/** POST /appointments/{id}/cancel (409 appointment-not-booked). */
export async function cancelAppointment(appointmentId: string, reason?: string): Promise<Appointment> {
  const body = reason ? { reason } : undefined;
  return (await request<Appointment>("POST", `/appointments/${seg(appointmentId)}/cancel`, body)).data;
}

/** GET /docks -> the inbound doors and the service's DOCK_DOOR_MODE. */
export async function listDocks(): Promise<DockList> {
  const res = (await request<DockList>("GET", "/docks")).data;
  return { mode: res.mode, items: res.items ?? [] };
}

/** A receipt as the UI uses it: the arrays are never undefined, even if the API leaves an empty one out. */
function normalizeReceipt(r: Receipt): Receipt {
  return { ...r, lines: r.lines ?? [], discrepancies: r.discrepancies ?? [] };
}

/** GET /receipts?asnNumber=&state=&limit=&cursor= */
export async function listReceipts(q: ListReceiptsQuery): Promise<ReceiptPage> {
  const res = await request<ReceiptPage>(
    "GET",
    withQuery("/receipts", { limit: q.limit, cursor: q.cursor, asnNumber: q.asnNumber, state: q.state }),
  );
  return { items: (res.data.items ?? []).map(normalizeReceipt), nextCursor: res.data.nextCursor || undefined };
}

/** GET /receipts/{receiptId} */
export async function getReceipt(receiptId: string): Promise<Receipt> {
  return normalizeReceipt((await request<Receipt>("GET", `/receipts/${seg(receiptId)}`)).data);
}

/** POST /receipts -> 201 (409 asn-not-receivable / receipt-already-open / appointment-not-checked-in). */
export async function openReceipt(input: OpenReceiptInput): Promise<Receipt> {
  return normalizeReceipt((await request<Receipt>("POST", "/receipts", input)).data);
}

/** POST /receipts/{id}/lines -> 201 with the receipt after the change (409 receipt-closed, 422 line-not-on-asn). */
export async function receiveLine(receiptId: string, input: ReceiveLineInput): Promise<Receipt> {
  return normalizeReceipt((await request<Receipt>("POST", `/receipts/${seg(receiptId)}/lines`, input)).data);
}

/** POST /receipts/{id}/close -> the closed receipt with its final discrepancies. */
export async function closeReceipt(receiptId: string): Promise<Receipt> {
  return normalizeReceipt((await request<Receipt>("POST", `/receipts/${seg(receiptId)}/close`)).data);
}
