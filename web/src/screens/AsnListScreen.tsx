import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listAsns } from "../api";
import { Form, FormRow, SelectField, SubmitButton } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel } from "../lib";
import { ASN_STATES } from "../types";
import type { Asn, AsnState } from "../types";

const STATE_OPTIONS = ASN_STATES.map((s) => ({ value: s, label: s }));
const SIZE_OPTIONS = ["25", "50", "100"].map((s) => ({ value: s, label: `${s} per page` }));

interface Filters {
  state: "" | AsnState;
  limit: string;
}

const INITIAL: Filters = { state: "", limit: "25" };

/**
 * ASN list: GET /asns in ascending ASN-number order, a page at a time,
 * optionally narrowed to one lifecycle state. Paging follows the opaque
 * `nextCursor`; the cursors already visited are kept so "Previous" walks back
 * without the API having to support it. Changing the filters starts again at
 * the first page.
 */
export function AsnListScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters>(INITIAL);
  // cursors[i] is the cursor of page i+1 ("" = the first page).
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () => listAsns({ limit: Number(applied.limit), cursor: cursor || undefined, state: applied.state || undefined }),
    `asns|${applied.state}|${applied.limit}|${cursor}`,
  );

  const apply = () => {
    setApplied(draft);
    setCursors([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Advance ship notices"
        subtitle="inbound-receiving · what suppliers announced, and where each delivery stands"
      />
      <Card title="Filters">
        <Form label="ASN filters" onSubmit={apply}>
          <FormRow>
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
          what="ASNs"
          isEmpty={(p) => p.items.length === 0}
          empty={cursors.length === 1 ? "No ASNs match these filters." : "No more ASNs on this page."}
        >
          {(p) => <AsnTable asns={p.items} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function AsnTable({ asns }: { asns: Asn[] }) {
  return (
    <DataTable
      rowKey={(a) => a.asnNumber}
      rows={asns}
      columns={[
        {
          key: "asn",
          header: "ASN",
          // Relative link: `..` is the parent route (this remote's mount
          // point), so it works under the console's /inbound-receiving/* and
          // standalone at /.
          render: (a) => <Link to={`../asns/${encodeURIComponent(a.asnNumber)}`}>{a.asnNumber}</Link>,
        },
        { key: "supplier", header: "Supplier", render: (a) => a.supplierRef },
        { key: "state", header: "State", render: (a) => <StatePill state={a.state} /> },
        { key: "arrival", header: "Expected arrival", render: (a) => utcLabel(a.expectedArrival) },
        { key: "lines", header: "Lines", align: "right", render: (a) => String(a.lines.length) },
        { key: "version", header: "Version", align: "right", render: (a) => String(a.version) },
      ]}
    />
  );
}
