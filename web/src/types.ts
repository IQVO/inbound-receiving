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
