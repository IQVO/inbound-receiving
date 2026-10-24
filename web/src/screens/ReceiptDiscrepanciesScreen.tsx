import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getReceipt, receiveScan, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SelectField, SubmitButton, TextField } from "../components/formkit";
import { CartonDiscrepancyPill, PageHeader, Stack } from "../components/layout";
import { EmptyNote, ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { DISCREPANCY_GUIDE, describeValue, parseSkuQty, problemLead } from "../cartons";
import { utcLabel, wholeNumber } from "../lib";
import { CARTON_DISCREPANCY_KINDS, SCAN_KINDS } from "../types";
import type { ReceiptCartonDiscrepancy, ReceiveScanInput, Receipt, ScanKind } from "../types";

/** The plate a finding is about: the SSCC, the manifest SSCC of a missing carton, or the internal plate of an unlabelled case. */
function cartonOf(f: ReceiptCartonDiscrepancy): string | undefined {
  if (f.sscc) return f.sscc;
  if (f.kind === "MissingCarton") return f.expected || undefined;
  if (f.kind === "UnlabelledCarton") return f.actual || undefined;
  return undefined;
}

const SCAN_LABEL: Record<ScanKind, string> = {
  SSCC: "Carton label (SSCC)",
  CASE_GTIN: "Case barcode (GTIN, counts cases)",
  EACH_GTIN: "Each barcode (GTIN, counts eaches)",
};

export function ReceiptDiscrepanciesRoute() {
  const { receiptId = "" } = useParams();
  return <ReceiptDiscrepanciesScreen key={receiptId} receiptId={receiptId} />;
}

/**
 * Carton discrepancies of one receipt, read from the receipt itself
 * (GET /receipts/{id} `cartonDiscrepancies`, ADR 0015): every finding raised
 * by a scan, by this screen or any other client, and the MissingCarton
 * findings raised at close. The scan form posts to /receipts/{id}/scans and
 * then re-reads the receipt, so the list never depends on this browser's
 * session. Each of the nine kinds is explained with what to do about it.
 */
export function ReceiptDiscrepanciesScreen({ receiptId }: { receiptId: string }) {
  const receipt = useRequest(() => getReceipt(receiptId), `receipt|${receiptId}`);
  return (
    <Stack gap={5}>
      <div>
        <Link to={`../receipts/${encodeURIComponent(receiptId)}`}>← {receiptId}</Link>
      </div>
      <RequestView state={receipt} what="receipt" lead={(e) => problemLead(e.slug)}>
        {(r) => <Body initial={r} />}
      </RequestView>
    </Stack>
  );
}

function Body({ initial }: { initial: Receipt }) {
  const [receipt, setReceipt] = useState(initial);
  const [refreshProblem, setRefreshProblem] = useState<ApiError | null>(null);
  const findings = receipt.cartonDiscrepancies ?? [];

  /** After a scan: read the receipt again, which now lists what the scan raised. */
  const refresh = async () => {
    try {
      setReceipt(await getReceipt(receipt.receiptId));
      setRefreshProblem(null);
    } catch (err) {
      setRefreshProblem(toApiError(err));
    }
  };

  return (
    <>
      <PageHeader title={`${receipt.receiptId} · carton discrepancies`} subtitle="inbound-receiving · findings from carton receiving" />
      {receipt.state === "Open" && <ScanCard receipt={receipt} onScanned={refresh} />}
      {refreshProblem && (
        <ProblemAlert error={refreshProblem} lead="The scan was sent, but the receipt could not be read again, so the list below may be out of date." />
      )}
      <FindingsCard findings={findings} receiptId={receipt.receiptId} />
      <GuideCard findings={findings} />
    </>
  );
}

function parseCounted(raw: string): { sku: string; qty: number }[] | string {
  const out: { sku: string; qty: number }[] = [];
  for (const part of raw.split(/[\s,]+/).filter((p) => p !== "")) {
    const p = parseSkuQty(part);
    if (!p) return `"${part}" is not SKU:quantity (for example SKU-1:20, in eaches).`;
    out.push(p);
  }
  return out;
}

/** POST /receipts/{id}/scans: one scan per submit, each with its own Idempotency-Key (minted by the api layer). */
function ScanCard({ receipt, onScanned }: { receipt: Receipt; onScanned: () => Promise<void> }) {
  const [kind, setKind] = useState<ScanKind>("SSCC");
  const [id, setId] = useState("");
  const [qty, setQty] = useState("");
  const [counted, setCounted] = useState("");
  const [invalid, setInvalid] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [result, setResult] = useState<{ accepted: boolean; findings: number } | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    setResult(null);
    if (id.trim() === "") return setInvalid("Scan or type the barcode digits.");
    const input: ReceiveScanInput = { kind, id: id.trim() };
    if (kind !== "SSCC" && qty.trim() !== "") {
      const n = wholeNumber(qty);
      if (n === null || n < 1) return setInvalid(`Enter a whole number of ${kind === "CASE_GTIN" ? "cases" : "eaches"}, 1 or more.`);
      input.qty = n;
    }
    if (kind === "SSCC" && counted.trim() !== "") {
      const c = parseCounted(counted);
      if (typeof c === "string") return setInvalid(c);
      input.countedContents = c;
    }
    setInvalid(null);
    setSaving(true);
    try {
      const res = await receiveScan(receipt.receiptId, input);
      setResult({ accepted: res.accepted, findings: res.discrepancies.length });
      await onScanned();
      setId("");
      setQty("");
      setCounted("");
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Scan a carton or case">
      <Form label="Scan" onSubmit={submit}>
        <FormRow>
          <SelectField
            label="Scan type"
            value={kind}
            onChange={(v) => setKind(v as ScanKind)}
            options={SCAN_KINDS.map((k) => ({ value: k, label: SCAN_LABEL[k] }))}
            required
          />
          <TextField label="Barcode" value={id} onChange={setId} inputMode="numeric" required />
          {kind !== "SSCC" && (
            <TextField
              label={kind === "CASE_GTIN" ? "Cases" : "Eaches"}
              value={qty}
              onChange={setQty}
              inputMode="numeric"
              hint="Blank counts 1"
            />
          )}
          {kind === "SSCC" && (
            <TextField label="Counted contents" value={counted} onChange={setCounted} hint="Mixed carton only: SKU:eaches, ..." />
          )}
          <SubmitButton disabled={saving}>{saving ? "Scanning…" : "Scan"}</SubmitButton>
        </FormRow>
        <InlineError message={invalid} />
        {problem && <ProblemAlert error={problem} lead={problemLead(problem.slug)} />}
        {result?.accepted && (
          <InlineSuccess message={`Accepted.${result.findings > 0 ? ` ${result.findings} finding(s) were raised, see below.` : ""}`} />
        )}
        {result && !result.accepted && (
          <InlineError message={`Not accepted: nothing was booked. ${result.findings} finding(s) were raised, see below.`} />
        )}
      </Form>
    </Card>
  );
}

function FindingsCard({ findings, receiptId }: { findings: ReceiptCartonDiscrepancy[]; receiptId: string }) {
  return (
    <Card title="Findings on this receipt">
      {findings.length === 0 ? (
        <EmptyNote>
          No carton findings on this receipt. Findings appear here as cartons are scanned, and announced cartons that never arrived
          appear once the receipt is closed.
        </EmptyNote>
      ) : (
        <DataTable
          rowKey={(f: ReceiptCartonDiscrepancy) => `${f.at}|${f.kind}|${cartonOf(f) ?? ""}|${f.expected ?? ""}|${f.actual ?? ""}`}
          rows={findings}
          columns={[
            {
              key: "kind",
              header: "Kind",
              render: (f) => <CartonDiscrepancyPill kind={f.kind} blocking={DISCREPANCY_GUIDE[f.kind].blocking} />,
            },
            { key: "sscc", header: "Carton", render: (f) => (cartonOf(f) ? <code>{cartonOf(f)}</code> : "—") },
            { key: "at", header: "Raised", render: (f) => utcLabel(f.at) },
            { key: "expected", header: "Expected", render: (f) => describeValue(f, "expected") },
            { key: "actual", header: "Actual", render: (f) => describeValue(f, "actual") },
            {
              key: "what",
              header: "What happened",
              render: (f) => DISCREPANCY_GUIDE[f.kind].what,
            },
            {
              key: "do",
              header: "What to do",
              render: (f) => (
                <div>
                  {DISCREPANCY_GUIDE[f.kind].action}
                  {f.kind === "UnlabelledCarton" && f.actual && (
                    <>
                      {" "}
                      <Link to={`../receipts/${encodeURIComponent(receiptId)}/cartons/${encodeURIComponent(f.actual)}/label`}>
                        Print label
                      </Link>
                    </>
                  )}
                </div>
              ),
            },
          ]}
        />
      )}
    </Card>
  );
}

/** All nine kinds, always: what each means, what to do, and how many of them this receipt has. */
function GuideCard({ findings }: { findings: ReceiptCartonDiscrepancy[] }) {
  return (
    <Card title="What each finding means">
      <DataTable
        rowKey={(k: (typeof CARTON_DISCREPANCY_KINDS)[number]) => k}
        rows={[...CARTON_DISCREPANCY_KINDS]}
        columns={[
          {
            key: "kind",
            header: "Kind",
            render: (k) => (
              <div>
                <strong>{DISCREPANCY_GUIDE[k].title}</strong>
                <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-xs)" }}>{k}</div>
              </div>
            ),
          },
          {
            key: "count",
            header: "On this receipt",
            align: "right",
            render: (k) => String(findings.filter((f) => f.kind === k).length),
          },
          { key: "what", header: "What it means", render: (k) => DISCREPANCY_GUIDE[k].what },
          { key: "do", header: "What to do", render: (k) => DISCREPANCY_GUIDE[k].action },
          {
            key: "blocking",
            header: "Blocks?",
            render: (k) => (DISCREPANCY_GUIDE[k].blocking ? "Goes to problem-solve" : "Informational"),
          },
        ]}
      />
    </Card>
  );
}
