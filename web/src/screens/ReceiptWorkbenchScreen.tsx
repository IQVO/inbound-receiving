import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { closeReceipt, getReceipt, receiveLine, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SelectField, SubmitButton, TextField } from "../components/formkit";
import { DiscrepancyPill, Facts, PageHeader, Stack, StatePill } from "../components/layout";
import { EmptyNote, ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { remainingQty, summarize, utcLabel, wholeNumber } from "../lib";
import { CONDITIONS } from "../types";
import type { Condition, Receipt } from "../types";

const MAX_QTY = 2147483647;
const CONDITION_OPTIONS = CONDITIONS.map((c) => ({ value: c, label: c }));

/** Reads :receiptId and keys the screen on it, so moving between receipts never carries form state over. */
export function ReceiptWorkbenchRoute() {
  const { receiptId = "" } = useParams();
  return <ReceiptWorkbenchScreen key={receiptId} receiptId={receiptId} />;
}

/**
 * The receipt workbench: count a delivery line by line (Good or Damaged), see
 * the discrepancies as they stand, and close the receipt. Receiving into a
 * line is not naturally idempotent, so every submit carries its own
 * Idempotency-Key and the Record button is disabled while one is in flight.
 * Closing is final, so it asks for confirmation with the discrepancy summary
 * in front of the operator.
 */
export function ReceiptWorkbenchScreen({ receiptId }: { receiptId: string }) {
  const loaded = useRequest(() => getReceipt(receiptId), `receipt|${receiptId}`);
  // The receipt as returned by the latest receive / close; supersedes the
  // initially loaded one so no refetch is needed after each action.
  const [current, setCurrent] = useState<Receipt | null>(null);

  return (
    <Stack gap={5}>
      <div>
        <Link to="../receipts">← All receipts</Link>
      </div>
      <RequestView state={loaded} what="receipt">
        {(initial) => <Workbench receipt={current ?? initial} onChange={setCurrent} />}
      </RequestView>
    </Stack>
  );
}

function Workbench({ receipt, onChange }: { receipt: Receipt; onChange: (r: Receipt) => void }) {
  const open = receipt.state === "Open";
  return (
    <>
      <PageHeader title={receipt.receiptId} subtitle="inbound-receiving · receipt workbench" />
      {!open && receipt.closedAt && (
        <InlineSuccess message={`This receipt was closed at ${utcLabel(receipt.closedAt)}; its discrepancies are final.`} />
      )}
      <Card title="Receipt">
        <Facts
          items={[
            { label: "State", value: <StatePill state={receipt.state} /> },
            {
              label: "ASN",
              value: <Link to={`../asns/${encodeURIComponent(receipt.asnNumber)}`}>{receipt.asnNumber}</Link>,
            },
            { label: "Door", value: receipt.doorCode ?? "Walk-in delivery (no appointment)" },
            { label: "Opened", value: utcLabel(receipt.openedAt) },
            { label: "Closed", value: utcLabel(receipt.closedAt) },
            { label: "Version", value: String(receipt.version) },
          ]}
        />
      </Card>
      <Card title="Lines">
        <DataTable
          rowKey={(l) => String(l.lineNo)}
          rows={receipt.lines}
          columns={[
            { key: "lineNo", header: "Line", align: "right", render: (l) => String(l.lineNo) },
            { key: "sku", header: "SKU", render: (l) => l.sku },
            { key: "expected", header: "Expected", align: "right", render: (l) => String(l.expectedQty) },
            { key: "good", header: "Good", align: "right", render: (l) => String(l.receivedGood) },
            { key: "damaged", header: "Damaged", align: "right", render: (l) => String(l.receivedDamaged) },
            {
              key: "remaining",
              header: "Remaining",
              align: "right",
              render: (l) => String(remainingQty(l.expectedQty, l.receivedGood, l.receivedDamaged)),
            },
          ]}
        />
      </Card>
      {open && <ReceiveCard receipt={receipt} onChange={onChange} />}
      <DiscrepancyCard receipt={receipt} />
      {open && <CloseCard receipt={receipt} onChange={onChange} />}
    </>
  );
}

/** POST /receipts/{id}/lines: records a quantity of one line as Good or Damaged. */
function ReceiveCard({ receipt, onChange }: { receipt: Receipt; onChange: (r: Receipt) => void }) {
  const [lineNo, setLineNo] = useState("");
  const [quantity, setQuantity] = useState("");
  const [condition, setCondition] = useState<Condition>("Good");
  const [invalid, setInvalid] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [recorded, setRecorded] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    setRecorded(null);
    const line = receipt.lines.find((l) => String(l.lineNo) === lineNo);
    if (!line) return setInvalid("Choose the line you are counting.");
    const qty = wholeNumber(quantity);
    if (qty === null || qty < 1 || qty > MAX_QTY) {
      return setInvalid(`The quantity must be a whole number from 1 to ${MAX_QTY}.`);
    }
    setInvalid(null);
    setSaving(true);
    try {
      const updated = await receiveLine(receipt.receiptId, { lineNo: line.lineNo, quantity: qty, condition });
      onChange(updated);
      setRecorded(`Recorded ${qty} ${condition} on line ${line.lineNo} (${line.sku}).`);
      setQuantity("");
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Receive a line">
      <Form label="Receive line" onSubmit={submit}>
        <FormRow>
          <SelectField
            label="Line"
            value={lineNo}
            onChange={setLineNo}
            options={receipt.lines.map((l) => ({
              value: String(l.lineNo),
              label: `${l.lineNo} · ${l.sku} (expected ${l.expectedQty})`,
            }))}
            required
          />
          <TextField label="Quantity" value={quantity} onChange={setQuantity} inputMode="numeric" required />
          <SelectField
            label="Condition"
            value={condition}
            onChange={(v) => setCondition(v as Condition)}
            options={CONDITION_OPTIONS}
            required
          />
          <SubmitButton disabled={saving}>{saving ? "Recording…" : "Record"}</SubmitButton>
        </FormRow>
        <InlineError message={invalid} />
        {problem && <ProblemAlert error={problem} />}
        {recorded && <InlineSuccess message={recorded} />}
      </Form>
    </Card>
  );
}

/**
 * The discrepancies as the API computes them: live while the receipt is Open
 * (what closing now would report; a line nothing was received for is Short)
 * and final once it is Closed.
 */
function DiscrepancyCard({ receipt }: { receipt: Receipt }) {
  const closed = receipt.state === "Closed";
  const summary = summarize(receipt.discrepancies);
  return (
    <Card title={closed ? "Discrepancy summary (final)" : "Discrepancies if closed now"}>
      {summary === null ? (
        <EmptyNote>No discrepancies: every line matches what was expected.</EmptyNote>
      ) : (
        <Stack gap={3}>
          <div role="status" style={{ fontWeight: 600 }}>
            {summary}
          </div>
          <DataTable
            rowKey={(d) => `${d.lineNo}|${d.kind}`}
            rows={receipt.discrepancies}
            columns={[
              { key: "lineNo", header: "Line", align: "right", render: (d) => String(d.lineNo) },
              { key: "sku", header: "SKU", render: (d) => d.sku },
              { key: "kind", header: "Kind", render: (d) => <DiscrepancyPill kind={d.kind} /> },
              { key: "expected", header: "Expected", align: "right", render: (d) => String(d.expectedQty) },
              { key: "received", header: "Received", align: "right", render: (d) => String(d.receivedQty) },
              { key: "damaged", header: "Damaged", align: "right", render: (d) => String(d.damagedQty) },
            ]}
          />
        </Stack>
      )}
    </Card>
  );
}

/** POST /receipts/{id}/close: final. Asks first, with the discrepancy summary in view. */
function CloseCard({ receipt, onChange }: { receipt: Receipt; onChange: (r: Receipt) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);
  const summary = summarize(receipt.discrepancies);

  const close = async () => {
    setProblem(null);
    setSaving(true);
    try {
      onChange(await closeReceipt(receipt.receiptId));
    } catch (err) {
      setProblem(toApiError(err));
      setSaving(false);
    }
  };

  return (
    <Card title="Close this receipt">
      <Stack gap={3}>
        {!confirming ? (
          <div>
            <SubmitButton type="button" onClick={() => setConfirming(true)}>
              Close receipt
            </SubmitButton>
          </div>
        ) : (
          <>
            <EmptyNote>
              Closing ends receiving, completes the ASN and cannot be undone.{" "}
              {summary === null ? "No discrepancies will be recorded." : `It will record: ${summary}.`}
            </EmptyNote>
            <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
              <SubmitButton type="button" disabled={saving} onClick={close}>
                {saving ? "Closing…" : "Confirm close"}
              </SubmitButton>
              <SubmitButton type="button" secondary disabled={saving} onClick={() => setConfirming(false)}>
                Keep receiving
              </SubmitButton>
            </div>
          </>
        )}
        {problem && <ProblemAlert error={problem} />}
      </Stack>
    </Card>
  );
}
