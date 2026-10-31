import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getAsnCartons } from "../api";
import { CartonStatusPill, Facts, PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { cartonKindLabel, eaches, groupByPallet, problemLead, statusSummary } from "../cartons";
import type { AsnCartonStatus, AsnCartons } from "../types";

/** Reads :asnNumber and keys the screen on it. */
export function AsnCartonTreeRoute() {
  const { asnNumber = "" } = useParams();
  return <AsnCartonTreeScreen key={asnNumber} asnNumber={asnNumber} />;
}

/**
 * The announced cartons of one ASN as a tree: ASN → pallet (SSCC) → carton
 * (SSCC) → contents per SKU, with each carton's status against the ASN's
 * receipt (Expected, Accepted, Missing). Quantities are always eaches; a
 * homogeneous case reads "Case of N eaches".
 */
export function AsnCartonTreeScreen({ asnNumber }: { asnNumber: string }) {
  const cartons = useRequest(() => getAsnCartons(asnNumber), `asn-cartons|${asnNumber}`);
  return (
    <Stack gap={5}>
      <div>
        <Link to={`../asns/${encodeURIComponent(asnNumber)}`}>← {asnNumber}</Link>
      </div>
      <RequestView state={cartons} what="cartons" lead={(e) => problemLead(e.slug)}>
        {(c) => <Tree data={c} />}
      </RequestView>
    </Stack>
  );
}

function Tree({ data }: { data: AsnCartons }) {
  const pallets = groupByPallet(data.cartons);
  const total = data.cartons.reduce((s, c) => s + c.contents.reduce((n, k) => n + k.qty, 0), 0);
  return (
    <>
      <PageHeader title={`${data.asnNumber} · carton tree`} subtitle="inbound-receiving · announced pallets and cartons" />
      <Card title="Summary">
        <Facts
          items={[
            { label: "ASN", value: data.asnNumber },
            { label: "Cartons", value: String(data.cartons.length) },
            { label: "Announced", value: eaches(total) },
            { label: "Status", value: statusSummary(data.cartons) || "—" },
            ...(data.ownerRef ? [{ label: "Owner", value: data.ownerRef }] : []),
          ]}
        />
      </Card>
      {pallets.length === 0 && (
        <Card title="Cartons">
          <div style={{ color: "var(--wh-color-text-muted)" }}>
            This ASN announced no cartons (a flat ASN): it is received by line, not by carton.
          </div>
        </Card>
      )}
      {pallets.map((p) => (
        <Card key={p.sscc ?? "loose"} title={p.sscc ? `Pallet ${p.sscc}` : "Cartons not on a pallet"}>
          <Stack gap={3}>
            <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)" }}>
              {p.cartons.length} {p.cartons.length === 1 ? "carton" : "cartons"} · {eaches(p.eaches)}
            </div>
            <DataTable
              rowKey={(c: AsnCartonStatus) => c.sscc}
              rows={p.cartons}
              columns={[
                { key: "sscc", header: "Carton SSCC", render: (c) => <code>{c.sscc}</code> },
                { key: "kind", header: "Type", render: (c) => cartonKindLabel(c.kind, c.contents) },
                {
                  key: "contents",
                  header: "Contents",
                  render: (c) => (
                    <ul style={{ margin: 0, paddingLeft: 16 }}>
                      {c.contents.map((k) => (
                        <li key={`${k.lineNo}|${k.sku}`}>
                          {k.sku}: {eaches(k.qty)}
                        </li>
                      ))}
                    </ul>
                  ),
                },
                { key: "status", header: "Status", render: (c) => <CartonStatusPill status={c.status} /> },
              ]}
            />
          </Stack>
        </Card>
      ))}
    </>
  );
}
