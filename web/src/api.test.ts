import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  bookAppointment,
  cancelAppointment,
  checkInAppointment,
  closeReceipt,
  getAsn,
  listAllAppointments,
  newIdempotencyKey,
  openReceipt,
  receiveLine,
  registerAsn,
  toApiError,
} from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { APPT_ID, RCPT_ID, appointment, asn, receipt } from "./test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("Idempotency-Key", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("every create POST carries a UUID Idempotency-Key and a JSON content type", async () => {
    const api = mockApi({
      "POST /asns": json(asn(), 201),
      "POST /appointments": json(appointment(), 201),
      "POST /receipts": json(receipt(), 201),
      [`POST /receipts/${RCPT_ID}/lines`]: json(receipt(), 201),
    });
    await registerAsn({ asnNumber: "ASN-1001", supplierRef: "ACME", lines: [{ lineNo: 1, sku: "SKU-1", expectedQty: 1 }] });
    await bookAppointment({
      doorCode: "D1",
      carrier: "C",
      windowStart: "2026-10-09T08:00:00Z",
      windowEnd: "2026-10-09T09:00:00Z",
      asnNumbers: ["ASN-1001"],
    });
    await openReceipt({ asnNumber: "ASN-1001" });
    await receiveLine(RCPT_ID, { lineNo: 1, quantity: 3, condition: "Good" });

    expect(api.calls).toHaveLength(4);
    for (const call of api.calls) {
      expect(call.method).toBe("POST");
      expect(call.headers["Idempotency-Key"]).toMatch(UUID);
      expect(call.headers["Content-Type"]).toBe("application/json");
    }
    expect(new Set(api.calls.map((c) => c.headers["Idempotency-Key"])).size).toBe(4);
  });

  it("two identical submits still use two different keys (one key per POST)", async () => {
    const api = mockApi({ [`POST /receipts/${RCPT_ID}/lines`]: json(receipt(), 201) });
    const input = { lineNo: 1, quantity: 3, condition: "Good" } as const;
    await receiveLine(RCPT_ID, input);
    await receiveLine(RCPT_ID, input);
    const [a, b] = api.calls.map((c) => c.headers["Idempotency-Key"]);
    expect(a).not.toBe(b);
  });

  it("the action POSTs send a key too, and a body-less POST sends no content type", async () => {
    const api = mockApi({
      [`POST /appointments/${APPT_ID}/check-in`]: json(appointment({ state: "CheckedIn" })),
      [`POST /appointments/${APPT_ID}/cancel`]: json(appointment({ state: "Cancelled" })),
      [`POST /receipts/${RCPT_ID}/close`]: json(receipt({ state: "Closed" })),
    });
    await checkInAppointment(APPT_ID);
    await cancelAppointment(APPT_ID);
    await closeReceipt(RCPT_ID);
    for (const call of api.calls) {
      expect(call.headers["Idempotency-Key"]).toMatch(UUID);
      expect(call.headers["Content-Type"]).toBeUndefined();
      expect(call.body).toBeUndefined();
    }
  });

  it("a cancel reason goes in the body", async () => {
    const api = mockApi({ [`POST /appointments/${APPT_ID}/cancel`]: json(appointment({ state: "Cancelled" })) });
    await cancelAppointment(APPT_ID, "Carrier delayed");
    expect(api.calls[0].body).toEqual({ reason: "Carrier delayed" });
  });

  it("GETs send no custom header (they stay simple CORS requests)", async () => {
    const api = mockApi({ "GET /asns/ASN-1001": json(asn()) });
    await getAsn("ASN-1001");
    expect(api.calls[0].headers).toEqual({});
  });

  it("falls back to getRandomValues when crypto.randomUUID is missing (plain-http origins)", () => {
    const real = globalThis.crypto;
    vi.stubGlobal("crypto", { getRandomValues: real.getRandomValues.bind(real) });
    expect(newIdempotencyKey()).toMatch(UUID);
  });
});

describe("errors", () => {
  it("turns a problem+json body into an ApiError with title, detail, status and slug", async () => {
    mockApi({
      "POST /asns": problem(409, "asn-already-exists", "ASN already exists", "an asn with this number is already registered"),
    });
    const err = await registerAsn({ asnNumber: "A", supplierRef: "S", lines: [] }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.title, e.detail, e.slug]).toEqual([
      409,
      "ASN already exists",
      "an asn with this number is already registered",
      "asn-already-exists",
    ]);
  });

  it("falls back to the status line when the error body is not JSON", async () => {
    mockApi({ "GET /asns/X": new Response("bad gateway", { status: 502, statusText: "Bad Gateway" }) });
    const e = (await getAsn("X").catch((x: unknown) => x)) as ApiError;
    expect([e.status, e.title, e.problem]).toEqual([502, "502 Bad Gateway", null]);
  });

  it("wraps a network failure (CORS, offline) as an ApiError with status 0", async () => {
    const e = toApiError(new TypeError("Failed to fetch"));
    expect([e.status, e.title, e.detail]).toEqual([0, "Network error", "Failed to fetch"]);
  });
});

describe("listAllAppointments", () => {
  it("follows nextCursor to the last page and sends the window", async () => {
    const api = mockApi({
      "GET /appointments": (call) =>
        call.query.get("cursor")
          ? json({ items: [appointment({ appointmentId: "appt-2" })] })
          : json({ items: [appointment()], nextCursor: "C2" }),
    });
    const all = await listAllAppointments({ from: "2026-10-09T00:00:00Z", to: "2026-10-10T00:00:00Z" });
    expect(all.map((a) => a.appointmentId)).toEqual([APPT_ID, "appt-2"]);
    expect(api.calls.map((c) => c.query.get("cursor"))).toEqual([null, "C2"]);
    expect(api.calls[0].query.get("from")).toBe("2026-10-09T00:00:00Z");
    expect(api.calls[0].query.get("to")).toBe("2026-10-10T00:00:00Z");
    expect(api.calls[0].query.get("limit")).toBe("500");
  });
});
