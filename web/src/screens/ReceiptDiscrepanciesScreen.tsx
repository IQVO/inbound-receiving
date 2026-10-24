import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getAsnCartons, getReceipt, receiveScan, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SelectField, SubmitButton, TextField } from "../components/formkit";
import { CartonDiscrepancyPill, PageHeader, Stack } from "../components/layout";
import { EmptyNote, ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { DISCREPANCY_GUIDE, describeValue, parseSkuQty, problemLead } from "../cartons";
import { wholeNumber } from "../lib";
import { CARTON_DISCREPANCY_KINDS, SCAN_KINDS } from "../types";
import type { CartonDiscrepancy, ReceiveScanInput, Receipt, ScanKind } from "../types";

/** A finding as this screen keeps it: the wire shape plus the plate to print a label for. */
export interface Finding extends CartonDiscrepancy {
  lpn?: string;
}

const storageKey = (receiptId: string) => `inbound-receiving.carton-findings.${receiptId}`;

function loadFindings(receiptId: string): Finding[] {
  try {
    const raw = sessionStorage.getItem(storageKey(receiptId));
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? (parsed as Finding[]) : [];
  } catch {
    return [];
  }
}

function saveFindings(receiptId: string, findings: Finding[]) {
  try {
    sessionStorage.setItem(storageKey(receiptId), JSON.stringify(findings));
  } catch {
    // storage full or blocked: the findings simply stay on screen for this visit
  }
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
 * Carton discrepancies of one receipt. The API raises them while a carton
 * is scanned (POST /receipts/{id}/scans answers them in the scan result) and
 * there is no endpoint that lists them afterwards, so this screen shows
 * (a) the findings of the scans made here, kept for the browser session, and
 * (b) MissingCarton for every announced carton the ASN endpoint reports as
 * Missing (a closed receipt that never accepted it). Each of the nine kinds
 * is explained with what to do about it.
 */
export function ReceiptDiscrepanciesScreen({ receiptId }: { receiptId: string }) {
  const receipt = useRequest(() => getReceipt(receiptId), `receipt|${receiptId}`);
  return (
    <Stack gap={5}>
      <div>
        <Link to={`../receipts/${encodeURIComponent(receiptId)}`}>← {receiptId}</Link>
      </div>
      <RequestView state={receipt} what="receipt" lead={(e) => problemLead(e.slug)}>
        {(r) => <Body receipt={r} />}
      </RequestView>
    </Stack>
  );
}

function Body({ receipt }: { receipt: Receipt }) {
  const [recorded, setRecorded] = useState<Finding[]>(() => loadFindings(receipt.receiptId));
  const cartons = useRequest(() => getAsnCartons(receipt.asnNumber), `receipt-cartons|${receipt.asnNumber}|${recorded.length}`);

  const missing: Finding[] =
    cartons.status === "success"
      ? cartons.data.cartons.filter((c) => c.status === "Missing").map((c) => ({ kind: "MissingCarton", sscc: c.sscc }))
      : [];
  const all = [...recorded, ...missing.filter((m) => !recorded.some((r) => r.kind === "MissingCarton" && r.sscc === m.sscc))];

  const add = (found: Finding[]) => {
    const next = [...recorded, ...found];
    setRecorded(next);
    saveFindings(receipt.receiptId, next);
  };

  return (
    <>
      <PageHeader title={`${receipt.receiptId} · carton discrepancies`} subtitle="inbound-receiving · findings from carton receiving" />
      {receipt.state === "Open" && <ScanCard receipt={receipt} onFindings={add} />}
      {cartons.status === "error" && (
        <ProblemAlert error={cartons.error} lead="Announced cartons could not be loaded, so missing cartons are not shown." />
      )}
      <FindingsCard findings={all} receiptId={receipt.receiptId} />
      <GuideCard findings={all} />
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
function ScanCard({ receipt, onFindings }: { receipt: Receipt; onFindings: (f: Finding[]) => void }) {
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
      const lpn = res.cartons.find((c) => !c.sscc)?.lpn;
      const found = res.discrepancies.map((d): Finding => ({ ...d, ...(d.kind === "UnlabelledCarton" && lpn ? { lpn } : {}) }));
      if (found.length > 0) onFindings(found);
      setResult({ accepted: res.accepted, findings: found.length });
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

function FindingsCard({ findings, receiptId }: { findings: Finding[]; receiptId: string }) {
  return (
    <Card title="Findings on this receipt">
      {findings.length === 0 ? (
        <EmptyNote>
          No carton findings to show. Findings appear here as cartons are scanned on this screen, and announced cartons that never
          arrived appear once the receipt is closed.
        </EmptyNote>
      ) : (
        <DataTable
          rowKey={(f: Finding) => `${f.kind}|${f.sscc ?? f.lpn ?? ""}|${f.expected ?? ""}|${f.actual ?? ""}`}
          rows={findings}
          columns={[
            {
              key: "kind",
              header: "Kind",
              render: (f) => <CartonDiscrepancyPill kind={f.kind} blocking={DISCREPANCY_GUIDE[f.kind].blocking} />,
            },
            { key: "sscc", header: "Carton", render: (f) => (f.sscc ? <code>{f.sscc}</code> : f.lpn ? <code>{f.lpn}</code> : "—") },
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
                  {f.kind === "UnlabelledCarton" && f.lpn && (
                    <>
                      {" "}
                      <Link to={`../receipts/${encodeURIComponent(receiptId)}/cartons/${encodeURIComponent(f.lpn)}/label`}>
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
function GuideCard({ findings }: { findings: Finding[] }) {
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
