import type { ReactNode } from "react";
import { StatusPill } from "@warehouse/ui-kit";
import type {
  AppointmentState,
  AsnState,
  CartonDiscrepancyKind,
  CartonStatus,
  DiscrepancyKind,
  ReceiptState,
} from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** These lifecycles are inbound-receiving's own, so StatusPill's tone override carries them. */
const STATE_TONE: Record<AsnState | AppointmentState | ReceiptState, Tone> = {
  Registered: "neutral",
  Receiving: "progress",
  Closed: "success",
  Cancelled: "danger",
  Booked: "neutral",
  CheckedIn: "progress",
  Completed: "success",
  Open: "progress",
};

export function StatePill({ state }: { state: AsnState | AppointmentState | ReceiptState }) {
  return <StatusPill status={state} tone={STATE_TONE[state]} size="sm" />;
}

const KIND_TONE: Record<DiscrepancyKind, Tone> = {
  Short: "warning",
  Over: "warning",
  Damaged: "danger",
};

export function DiscrepancyPill({ kind }: { kind: DiscrepancyKind }) {
  return <StatusPill status={kind} tone={KIND_TONE[kind]} size="sm" />;
}

const CARTON_STATUS_TONE: Record<CartonStatus, Tone> = {
  Expected: "neutral",
  Accepted: "success",
  Missing: "danger",
};

export function CartonStatusPill({ status }: { status: CartonStatus }) {
  return <StatusPill status={status} tone={CARTON_STATUS_TONE[status]} size="sm" />;
}

/** Informational findings (UnlabelledCarton, CartonOverLimit) are warnings; the rest block and are danger. */
export function CartonDiscrepancyPill({ kind, blocking }: { kind: CartonDiscrepancyKind; blocking: boolean }) {
  return <StatusPill status={kind} tone={blocking ? "danger" : "warning"} size="sm" />;
}
