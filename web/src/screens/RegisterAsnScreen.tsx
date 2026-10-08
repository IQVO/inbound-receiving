import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { registerAsn, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack } from "../components/layout";
import { ProblemAlert } from "../components/ProblemAlert";
import { asnNumberProblem, skuProblem, utcInputToIso, wholeNumber } from "../lib";
import type { Asn, RegisterAsnInput } from "../types";

const MAX_QTY = 2147483647;

interface LineDraft {
  id: number;
  sku: string;
  qty: string;
}

/**
 * POST /asns: registers the supplier's announcement of a delivery. The line
 * numbers are not typed: the API requires them to run exactly 1..n in order,
 * so they are the rows' positions. Each submit sends a fresh Idempotency-Key.
 */
export function RegisterAsnScreen() {
  const nextId = useRef(2);
  const [asnNumber, setAsnNumber] = useState("");
  const [supplierRef, setSupplierRef] = useState("");
  const [arrival, setArrival] = useState("");
  const [lines, setLines] = useState<LineDraft[]>([{ id: 1, sku: "", qty: "" }]);
  const [invalid, setInvalid] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [created, setCreated] = useState<Asn | null>(null);
  const [saving, setSaving] = useState(false);

  const setLine = (id: number, patch: Partial<LineDraft>) =>
    setLines((ls) => ls.map((l) => (l.id === id ? { ...l, ...patch } : l)));

  const validate = (): { why: string } | { input: RegisterAsnInput } => {
    const number = asnNumber.trim();
    const numberWhy = asnNumberProblem(number);
    if (numberWhy) return { why: numberWhy };
    const supplier = supplierRef.trim();
    if (supplier === "") return { why: "Enter the supplier reference." };
    if (supplier.length > 64) return { why: "A supplier reference is at most 64 characters." };
    let expectedArrival: string | undefined;
    if (arrival.trim() !== "") {
      const iso = utcInputToIso(arrival);
      if (!iso) return { why: "Enter a valid expected arrival (UTC)." };
      expectedArrival = iso;
    }
    const seen = new Set<string>();
    const out: { lineNo: number; sku: string; expectedQty: number }[] = [];
    for (const [i, l] of lines.entries()) {
      const sku = l.sku.trim();
      const skuWhy = skuProblem(sku);
      if (skuWhy) return { why: `Line ${i + 1}: ${skuWhy}` };
      if (seen.has(sku)) return { why: `Line ${i + 1}: SKU ${sku} is already on another line.` };
      seen.add(sku);
      const qty = wholeNumber(l.qty);
      if (qty === null || qty < 1 || qty > MAX_QTY) {
        return { why: `Line ${i + 1}: the expected quantity must be a whole number from 1 to ${MAX_QTY}.` };
      }
      out.push({ lineNo: i + 1, sku, expectedQty: qty });
    }
    return { input: { asnNumber: number, supplierRef: supplier, expectedArrival, lines: out } };
  };

  const submit = async () => {
    setProblem(null);
    setCreated(null);
    const checked = validate();
    if ("why" in checked) return setInvalid(checked.why);
    setInvalid(null);
    setSaving(true);
    try {
      const res = await registerAsn(checked.input);
      setCreated(res.data);
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Register ASN"
        subtitle="inbound-receiving · record a supplier's announced delivery; it starts in state Registered"
      />
      <Card title="Advance ship notice">
        <Form label="Register ASN" onSubmit={submit}>
          <FormRow>
            <TextField
              label="ASN number"
              value={asnNumber}
              onChange={setAsnNumber}
              required
              hint="1 to 64 characters of letters, digits, '.', '_' or '-'"
            />
            <TextField label="Supplier reference" value={supplierRef} onChange={setSupplierRef} required />
            <TextField
              label="Expected arrival (UTC)"
              type="datetime-local"
              value={arrival}
              onChange={setArrival}
              hint="Optional"
            />
          </FormRow>
          <fieldset
            style={{
              border: "1px solid var(--wh-color-border)",
              borderRadius: "var(--wh-radius-md)",
              display: "flex",
              flexDirection: "column",
              gap: "var(--wh-space-3)",
              padding: "var(--wh-space-3)",
            }}
          >
            <legend style={{ fontSize: "var(--wh-font-size-sm)", fontWeight: 600 }}>Lines</legend>
            {lines.map((l, i) => (
              <FormRow key={l.id}>
                <TextField
                  label={`SKU (line ${i + 1})`}
                  value={l.sku}
                  onChange={(v) => setLine(l.id, { sku: v })}
                  required
                />
                <TextField
                  label={`Expected quantity (line ${i + 1})`}
                  value={l.qty}
                  onChange={(v) => setLine(l.id, { qty: v })}
                  inputMode="numeric"
                  required
                />
                <SubmitButton
                  type="button"
                  secondary
                  disabled={lines.length === 1}
                  ariaLabel={`Remove line ${i + 1}`}
                  onClick={() => setLines((ls) => ls.filter((x) => x.id !== l.id))}
                >
                  Remove
                </SubmitButton>
              </FormRow>
            ))}
            <div>
              <SubmitButton
                type="button"
                secondary
                onClick={() => setLines((ls) => [...ls, { id: nextId.current++, sku: "", qty: "" }])}
              >
                Add line
              </SubmitButton>
            </div>
          </fieldset>
          <div>
            <SubmitButton disabled={saving}>{saving ? "Registering…" : "Register ASN"}</SubmitButton>
          </div>
          <InlineError message={invalid} />
          {problem && <ProblemAlert error={problem} />}
          {created && (
            <InlineSuccess
              message={
                <span>
                  Registered {created.asnNumber} with {created.lines.length} line
                  {created.lines.length === 1 ? "" : "s"} (version {created.version}).{" "}
                  <Link to={`../asns/${encodeURIComponent(created.asnNumber)}`}>Open {created.asnNumber}</Link>
                </span>
              }
            />
          )}
        </Form>
      </Card>
    </Stack>
  );
}
