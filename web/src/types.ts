/**
 * Wire shapes of inbound-receiving's REST API (apis/openapi.yaml). JSON is
 * camelCase; optional parts of a resource are OMITTED, never null.
 */

export const ASN_STATES = ["Registered", "Receiving", "Closed", "Cancelled"] as const;
export type AsnState = (typeof ASN_STATES)[number];

export const APPOINTMENT_STATES = ["Booked", "CheckedIn", "Completed", "Cancelled"] as const;
export type AppointmentState = (typeof APPOINTMENT_STATES)[number];

export const RECEIPT_STATES = ["Open", "Closed"] as const;
export type ReceiptState = (typeof RECEIPT_STATES)[number];

export const CONDITIONS = ["Good", "Damaged"] as const;
export type Condition = (typeof CONDITIONS)[number];

export type DiscrepancyKind = "Short" | "Over" | "Damaged";

export interface AsnLine {
  lineNo: number;
  sku: string;
  expectedQty: number;
}

export interface Asn {
  asnNumber: string;
  supplierRef: string;
  /** Omitted when the supplier gave none. */
  expectedArrival?: string;
  state: AsnState;
  lines: AsnLine[];
  version: number;
}

export interface AsnPage {
  items: Asn[];
  /** Absent on the last page. */
  nextCursor?: string;
}

export interface Appointment {
  appointmentId: string;
  doorCode: string;
  carrier: string;
  windowStart: string;
  windowEnd: string;
  asnNumbers: string[];
  state: AppointmentState;
  version: number;
}

export interface AppointmentPage {
  items: Appointment[];
  nextCursor?: string;
}

export interface ReceiptLine {
  lineNo: number;
  sku: string;
  expectedQty: number;
  receivedGood: number;
  receivedDamaged: number;
}

export interface Discrepancy {
  lineNo: number;
  sku: string;
  kind: DiscrepancyKind;
  expectedQty: number;
  /** Good plus damaged. */
  receivedQty: number;
  damagedQty: number;
}

export interface Receipt {
  receiptId: string;
  asnNumber: string;
  /** Omitted for a walk-in delivery. */
  appointmentId?: string;
  /** Omitted when the receipt has no appointment. */
  doorCode?: string;
  state: ReceiptState;
  lines: ReceiptLine[];
  openedAt: string;
  /** Present once the receipt is Closed. */
  closedAt?: string;
  discrepancies: Discrepancy[];
  version: number;
}

export interface ReceiptPage {
  items: Receipt[];
  nextCursor?: string;
}

export interface Dock {
  doorCode: string;
  dockFlow: "Inbound" | "Both";
}

export interface DockList {
  /** The service's current DOCK_DOOR_MODE; "permissive" = any door code can be booked. */
  mode: "kafka" | "permissive";
  items: Dock[];
}

export interface ListAsnsQuery {
  limit?: number;
  cursor?: string;
  state?: AsnState;
}

export interface ListAppointmentsQuery {
  limit?: number;
  cursor?: string;
  door?: string;
  state?: AppointmentState;
  /** RFC 3339: only appointments whose window ends after it. */
  from?: string;
  /** RFC 3339: only appointments whose window starts before it. */
  to?: string;
}

export interface ListReceiptsQuery {
  limit?: number;
  cursor?: string;
  asnNumber?: string;
  state?: ReceiptState;
}

export interface RegisterAsnInput {
  asnNumber: string;
  supplierRef: string;
  expectedArrival?: string;
  lines: AsnLine[];
}

export interface BookAppointmentInput {
  doorCode: string;
  carrier: string;
  windowStart: string;
  windowEnd: string;
  asnNumbers: string[];
}

export interface OpenReceiptInput {
  asnNumber: string;
  appointmentId?: string;
}

export interface ReceiveLineInput {
  lineNo: number;
  quantity: number;
  condition: Condition;
}

export interface ReasonInput {
  reason?: string;
}

/* ---- Cartons (ADR 0012-0014): hierarchical ASN, scan receiving, labels ---- */

export const CARTON_KINDS = ["HOMOGENEOUS_CASE", "MIXED"] as const;
export type CartonKind = (typeof CARTON_KINDS)[number];

/** What the ASN's receipt has done with an announced carton. */
export type CartonStatus = "Expected" | "Accepted" | "Missing";

export interface CartonContent {
  lineNo: number;
  sku: string;
  gtin: string;
  /** Eaches. */
  qty: number;
  lot?: string;
  expiry?: string;
}

export interface CartonDims {
  lengthMm: number;
  widthMm: number;
  heightMm: number;
}

/** One announced carton with its status (GET /asns/{asnNumber}/cartons). */
export interface AsnCartonStatus {
  sscc: string;
  /** The pallet SSCC, when the carton is on one. */
  parentSscc?: string;
  kind: CartonKind;
  contents: CartonContent[];
  grossWeightG?: number;
  dims?: CartonDims;
  status: CartonStatus;
}

export interface AsnCartons {
  asnNumber: string;
  ownerRef?: string;
  cartons: AsnCartonStatus[];
}

export const CARTON_DISCREPANCY_KINDS = [
  "UnexpectedCarton",
  "MissingCarton",
  "DuplicateSscc",
  "UnknownGtin",
  "CaseQtyMismatch",
  "MixedCaseDeclaredAsCasePack",
  "UnlabelledCarton",
  "CartonOverLimit",
  "MixedCartonContentMismatch",
] as const;
export type CartonDiscrepancyKind = (typeof CARTON_DISCREPANCY_KINDS)[number];

export interface CartonDiscrepancy {
  kind: CartonDiscrepancyKind;
  sscc?: string;
  expected?: string;
  actual?: string;
}

export const SCAN_KINDS = ["SSCC", "CASE_GTIN", "EACH_GTIN"] as const;
export type ScanKind = (typeof SCAN_KINDS)[number];

export interface ReceiveScanInput {
  kind: ScanKind;
  id: string;
  /** Cases for CASE_GTIN, eaches for EACH_GTIN; ignored for an SSCC. */
  qty?: number;
  condition?: Condition;
  countedContents?: { sku: string; qty: number }[];
}

export interface ReceiptCartonContent extends CartonContent {
  condition: Condition;
}

export interface ReceiptCarton {
  lpn: string;
  sscc?: string;
  parentSscc?: string;
  kind: CartonKind;
  status: "Expected" | "Accepted";
  sealed: boolean;
  contents: ReceiptCartonContent[];
  receivedAt?: string;
}

/** 201 = accepted, 200 = refused with findings (nothing booked). */
export interface ScanResult {
  kind: ScanKind;
  accepted: boolean;
  lines: ReceiptLine[];
  cartons: ReceiptCarton[];
  discrepancies: CartonDiscrepancy[];
  version: number;
}

export interface CartonLabel {
  lpn: string;
  symbology: "CODE128";
  /** The text printed under the barcode: "(00)<sscc>" or the LPN. */
  humanReadable: string;
  /** Omitted when the carton holds more than one SKU. */
  sku?: string;
  /** Eaches in the carton. */
  units: number;
  receivedAt: string;
}
