import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { cancelAsn, getAsn, listAllAppointments, listReceipts, openReceipt, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineSuccess, SelectField, SubmitButton, TextField } from "../components/formkit";
import { Facts, PageHeader, Stack, StatePill } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel, utcTime } from "../lib";
import type { Asn } from "../types";

/** Reads :asnNumber and keys the screen on it, so moving between ASNs never carries form state over. */
export function AsnDetailRoute() {
  const { asnNumber = "" } = useParams();
  return <AsnDetailScreen key={asnNumber} asnNumber={asnNumber} />;
}

/**
 * One ASN: its lines and lifecycle, the receipts counted against it, and the
 * two things an operator does to it from here: cancel it (only while it is
 * Registered; the API refuses an ASN that is being received) and open a
 * receipt for it (from a checked-in appointment that covers it, or as a
 * walk-in delivery).
 */
export function AsnDetailScreen({ asnNumber }: { asnNumber: string }) {
  const asn = useRequest(() => getAsn(asnNumber), `asn|${asnNumber}`);
  return (
    <Stack gap={5}>
      <div>
        <Link to="../asns">← All ASNs</Link>
      </div>
      <RequestView state={asn} what="ASN">
        {(a) => <AsnBody asn={a} reload={asn.reload} />}
      </RequestView>
    </Stack>
  );
}

function AsnBody({ asn, reload }: { asn: Asn; reload: () => void }) {
  const receivable = asn.state === "Registered" || asn.state === "Receiving";
  return (
    <>
      <PageHeader title={asn.asnNumber} subtitle="inbound-receiving · advance ship notice" />
      <Card title="ASN">
        <Facts
          items={[
            { label: "State", value: <StatePill state={asn.state} /> },
            { label: "Supplier reference", value: asn.supplierRef },
            { label: "Expected arrival", value: utcLabel(asn.expectedArrival) },
            { label: "Version", value: String(asn.version) },
          ]}
        />
      </Card>
      <Card title="Lines">
        <DataTable
          rowKey={(l) => String(l.lineNo)}
          rows={asn.lines}
          columns={[
            { key: "lineNo", header: "Line", align: "right", render: (l) => String(l.lineNo) },
            { key: "sku", header: "SKU", render: (l) => l.sku },
            { key: "qty", header: "Expected quantity", align: "right", render: (l) => String(l.expectedQty) },
          ]}
        />
      </Card>
      <ReceiptsCard asnNumber={asn.asnNumber} />
      {receivable && <OpenReceiptCard asnNumber={asn.asnNumber} />}
      {asn.state === "Registered" && <CancelAsnCard asnNumber={asn.asnNumber} onCancelled={reload} />}
    </>
  );
}

function ReceiptsCard({ asnNumber }: { asnNumber: string }) {
  const receipts = useRequest(() => listReceipts({ asnNumber, limit: 100 }), `asn-receipts|${asnNumber}`);
  return (
    <Card title="Receipts">
      <RequestView
        state={receipts}
        what="receipts"
        isEmpty={(p) => p.items.length === 0}
        empty="Nothing has been received against this ASN yet."
      >
        {(p) => (
          <DataTable
            rowKey={(r) => r.receiptId}
            rows={p.items}
            columns={[
              {
                key: "id",
                header: "Receipt",
                render: (r) => <Link to={`../receipts/${encodeURIComponent(r.receiptId)}`}>{r.receiptId}</Link>,
              },
              { key: "state", header: "State", render: (r) => <StatePill state={r.state} /> },
              { key: "door", header: "Door", render: (r) => r.doorCode ?? "walk-in" },
              { key: "opened", header: "Opened", render: (r) => utcLabel(r.openedAt) },
            ]}
          />
        )}
      </RequestView>
    </Card>
  );
}

/**
 * POST /receipts: opens a receipt against this ASN. With an appointment the
 * API requires it to be CheckedIn and to cover the ASN (and takes the door
 * from it); without one it is a walk-in delivery.
 */
function OpenReceiptCard({ asnNumber }: { asnNumber: string }) {
  const navigate = useNavigate();
  const appointments = useRequest(
    () => listAllAppointments({ state: "CheckedIn" }),
    `asn-checked-in|${asnNumber}`,
  );
  const [appointmentId, setAppointmentId] = useState("");
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);

  const options =
    appointments.status === "success"
      ? appointments.data
          .filter((a) => a.asnNumbers.includes(asnNumber))
          .map((a) => ({
            value: a.appointmentId,
            label: `${a.doorCode} · ${utcTime(a.windowStart)}–${utcTime(a.windowEnd)}Z · ${a.carrier}`,
          }))
      : [];

  const submit = async () => {
    setProblem(null);
    setSaving(true);
    try {
      const receipt = await openReceipt({ asnNumber, ...(appointmentId ? { appointmentId } : {}) });
      void navigate(`../receipts/${encodeURIComponent(receipt.receiptId)}`);
    } catch (err) {
      setProblem(toApiError(err));
      setSaving(false);
    }
  };

  return (
    <Card title="Open a receipt">
      <Form label="Open receipt" onSubmit={submit}>
        <FormRow>
          <SelectField
            label="Appointment"
            value={appointmentId}
            onChange={setAppointmentId}
            options={options}
            emptyLabel="Walk-in delivery (no appointment)"
          />
          <SubmitButton disabled={saving}>{saving ? "Opening…" : "Open receipt"}</SubmitButton>
        </FormRow>
        {appointments.status === "error" && <ProblemAlert error={appointments.error} lead="Checked-in appointments could not be loaded." />}
        {problem && <ProblemAlert error={problem} />}
      </Form>
    </Card>
  );
}

/** POST /asns/{asnNumber}/cancel; refused (409) once the ASN is being received or terminal. */
function CancelAsnCard({ asnNumber, onCancelled }: { asnNumber: string; onCancelled: () => void }) {
  const [reason, setReason] = useState("");
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [done, setDone] = useState(false);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    setSaving(true);
    try {
      await cancelAsn(asnNumber, reason.trim() || undefined);
      setDone(true);
      onCancelled();
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Cancel this ASN">
      <Form label="Cancel ASN" onSubmit={submit}>
        <FormRow>
          <TextField label="Reason" value={reason} onChange={setReason} hint="Optional, at most 200 characters" />
          <SubmitButton disabled={saving}>{saving ? "Cancelling…" : "Cancel ASN"}</SubmitButton>
        </FormRow>
        {problem && <ProblemAlert error={problem} />}
        {done && <InlineSuccess message={`${asnNumber} was cancelled.`} />}
      </Form>
    </Card>
  );
}
