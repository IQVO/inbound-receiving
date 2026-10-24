import type { Appointment, Asn, AsnCartons, AsnCartonStatus, CartonLabel, Dock, DockList, Receipt, ScanResult } from "../types";

export const APPT_ID = "appt-123e4567-e89b-12d3-a456-426614174000";
export const RCPT_ID = "rcpt-223e4567-e89b-12d3-a456-426614174000";

/** The openapi.yaml registerAsn example. */
export function asn(over: Partial<Asn> = {}): Asn {
  return {
    asnNumber: "ASN-1001",
    supplierRef: "ACME",
    expectedArrival: "2026-10-09T08:00:00Z",
    state: "Registered",
    lines: [
      { lineNo: 1, sku: "SKU-1", expectedQty: 40 },
      { lineNo: 2, sku: "SKU-2", expectedQty: 5 },
    ],
    version: 1,
    ...over,
  };
}

/** Booked on door 01, 08:00-10:00 UTC on 2026-10-09. */
export function appointment(over: Partial<Appointment> = {}): Appointment {
  return {
    appointmentId: APPT_ID,
    doorCode: "WH1-DOCK-IN-01",
    carrier: "ACME Freight",
    windowStart: "2026-10-09T08:00:00Z",
    windowEnd: "2026-10-09T10:00:00Z",
    asnNumbers: ["ASN-1001"],
    state: "Booked",
    version: 1,
    ...over,
  };
}

/** An Open receipt with nothing counted yet (the openReceipt example). */
export function receipt(over: Partial<Receipt> = {}): Receipt {
  return {
    receiptId: RCPT_ID,
    asnNumber: "ASN-1001",
    appointmentId: APPT_ID,
    doorCode: "WH1-DOCK-IN-01",
    state: "Open",
    lines: [
      { lineNo: 1, sku: "SKU-1", expectedQty: 40, receivedGood: 0, receivedDamaged: 0 },
      { lineNo: 2, sku: "SKU-2", expectedQty: 5, receivedGood: 0, receivedDamaged: 0 },
    ],
    openedAt: "2026-10-09T08:05:00Z",
    discrepancies: [],
    cartonDiscrepancies: [],
    version: 1,
    ...over,
  };
}

/** The closeReceipt example: line 1 short + damaged, line 2 over. */
export function closedReceipt(over: Partial<Receipt> = {}): Receipt {
  return receipt({
    state: "Closed",
    lines: [
      { lineNo: 1, sku: "SKU-1", expectedQty: 40, receivedGood: 30, receivedDamaged: 4 },
      { lineNo: 2, sku: "SKU-2", expectedQty: 5, receivedGood: 7, receivedDamaged: 0 },
    ],
    closedAt: "2026-10-09T09:30:00Z",
    discrepancies: [
      { lineNo: 1, sku: "SKU-1", kind: "Short", expectedQty: 40, receivedQty: 34, damagedQty: 4 },
      { lineNo: 1, sku: "SKU-1", kind: "Damaged", expectedQty: 40, receivedQty: 34, damagedQty: 4 },
      { lineNo: 2, sku: "SKU-2", kind: "Over", expectedQty: 5, receivedQty: 7, damagedQty: 0 },
    ],
    version: 6,
    ...over,
  });
}

export const DOCKS: Dock[] = [
  { doorCode: "WH1-DOCK-IN-01", dockFlow: "Inbound" },
  { doorCode: "WH1-DOCK-IO-02", dockFlow: "Both" },
];

export function docks(mode: DockList["mode"] = "kafka"): DockList {
  return { mode, items: DOCKS };
}

/* ---- cartons ---- */

export const SSCC_1 = "340123450000000017";
export const SSCC_2 = "340123450000000024";
export const PALLET = "340123450000000093";

export function cartonStatus(over: Partial<AsnCartonStatus> = {}): AsnCartonStatus {
  return {
    sscc: SSCC_1,
    parentSscc: PALLET,
    kind: "HOMOGENEOUS_CASE",
    contents: [{ lineNo: 1, sku: "SKU-1", gtin: "00614141000036", qty: 24 }],
    status: "Expected",
    ...over,
  };
}

export function asnCartons(over: Partial<AsnCartons> = {}): AsnCartons {
  return {
    asnNumber: "ASN-1001",
    cartons: [
      cartonStatus(),
      cartonStatus({
        sscc: SSCC_2,
        kind: "MIXED",
        contents: [
          { lineNo: 1, sku: "SKU-1", gtin: "00614141000036", qty: 4 },
          { lineNo: 2, sku: "SKU-2", gtin: "00614141000043", qty: 1 },
        ],
        status: "Accepted",
      }),
      cartonStatus({ sscc: "340123450000000031", parentSscc: undefined, status: "Missing" }),
    ],
    ...over,
  };
}

export const LPN = "LPN-01K8Z3Q5R7T9V1W2X4Y6A8B0CD";

/** The openapi getCartonLabel example. */
export function cartonLabel(over: Partial<CartonLabel> = {}): CartonLabel {
  return {
    lpn: LPN,
    symbology: "CODE128",
    humanReadable: LPN,
    sku: "SKU-1",
    units: 20,
    receivedAt: "2026-10-09T08:10:00Z",
    ...over,
  };
}

export function scanResult(over: Partial<ScanResult> = {}): ScanResult {
  return { kind: "SSCC", accepted: true, lines: [], cartons: [], discrepancies: [], version: 2, ...over };
}
