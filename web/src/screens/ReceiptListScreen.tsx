import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listReceipts } from "../api";
import { Form, FormRow, SelectField, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel } from "../lib";
import { RECEIPT_STATES } from "../types";
import type { Receipt, ReceiptState } from "../types";

const STATE_OPTIONS = RECEIPT_STATES.map((s) => ({ value: s, label: s }));
const SIZE_OPTIONS = ["25", "50", "100"].map((s) => ({ value: s, label: `${s} per page` }));

interface Filters {
  asnNumber: string;
  state: "" | ReceiptState;
  limit: string;
}

const INITIAL: Filters = { asnNumber: "", state: "", limit: "25" };

/**
 * Receipt list: GET /receipts in ascending receipt-id order, a page at a
 * time, optionally narrowed to one ASN and/or state. Receipts are OPENED from
 * an ASN's page (that is where the appointment is chosen), so this screen is
 * for finding one to continue counting or to look at its discrepancies.
 */
export function ReceiptListScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters>(INITIAL);
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () =>
      listReceipts({
        limit: Number(applied.limit),
        cursor: cursor || undefined,
        asnNumber: applied.asnNumber.trim() || undefined,
        state: applied.state || undefined,
      }),
    `receipts|${applied.asnNumber}|${applied.state}|${applied.limit}|${cursor}`,
  );

  const apply = () => {
    setApplied(draft);
    setCursors([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Receipts"
        subtitle="inbound-receiving · counted receiving of each ASN and what was found at close"
      />
      <Card title="Filters">
        <Form label="Receipt filters" onSubmit={apply}>
          <FormRow>
            <TextField
              label="ASN number"
              value={draft.asnNumber}
              onChange={(v) => setDraft({ ...draft, asnNumber: v })}
              placeholder="ASN-1001"
            />
            <SelectField
              label="State"
              value={draft.state}
              onChange={(v) => setDraft({ ...draft, state: v as Filters["state"] })}
              options={STATE_OPTIONS}
              emptyLabel="Any state"
            />
            <SelectField
              label="Page size"
              value={draft.limit}
              onChange={(v) => setDraft({ ...draft, limit: v })}
              options={SIZE_OPTIONS}
            />
            <SubmitButton>Apply filters</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <Card
        title={`Page ${cursors.length}`}
        actions={
          <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
            <SubmitButton
              type="button"
              secondary
              disabled={cursors.length === 1 || page.status === "loading"}
              onClick={() => setCursors(cursors.slice(0, -1))}
            >
              Previous page
            </SubmitButton>
            <SubmitButton
              type="button"
              secondary
              disabled={page.status !== "success" || !page.data.nextCursor}
              onClick={() => {
                if (page.status === "success" && page.data.nextCursor) {
                  setCursors([...cursors, page.data.nextCursor]);
                }
              }}
            >
              Next page
            </SubmitButton>
          </div>
        }
      >
        <RequestView
          state={page}
          what="receipts"
          isEmpty={(p) => p.items.length === 0}
          empty={cursors.length === 1 ? "No receipts match these filters." : "No more receipts on this page."}
        >
          {(p) => <ReceiptTable receipts={p.items} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function ReceiptTable({ receipts }: { receipts: Receipt[] }) {
  return (
    <DataTable
      rowKey={(r) => r.receiptId}
      rows={receipts}
      columns={[
        {
          key: "id",
          header: "Receipt",
          render: (r) => <Link to={`../receipts/${encodeURIComponent(r.receiptId)}`}>{r.receiptId}</Link>,
        },
        {
          key: "asn",
          header: "ASN",
          render: (r) => <Link to={`../asns/${encodeURIComponent(r.asnNumber)}`}>{r.asnNumber}</Link>,
        },
        { key: "state", header: "State", render: (r) => <StatePill state={r.state} /> },
        { key: "door", header: "Door", render: (r) => r.doorCode ?? "walk-in" },
        { key: "opened", header: "Opened", render: (r) => utcLabel(r.openedAt) },
        { key: "closed", header: "Closed", render: (r) => utcLabel(r.closedAt) },
        {
          key: "disc",
          header: "Discrepancies",
          align: "right",
          render: (r) => String(r.discrepancies.length),
        },
      ]}
    />
  );
}
