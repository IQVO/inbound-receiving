import { Link, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { getCartonLabel } from "../api";
import { SubmitButton } from "../components/formkit";
import { PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { barcodeData, code128Bars, eaches, problemLead } from "../cartons";
import { utcLabel } from "../lib";
import type { CartonLabel } from "../types";

/** The print stylesheet: only the label sheet prints, on its own, at a fixed 100 x 150 mm. */
const PRINT_CSS = `
@media print {
  @page { size: 100mm 150mm; margin: 4mm; }
  body * { visibility: hidden !important; }
  .ir-label-sheet, .ir-label-sheet * { visibility: visible !important; }
  .ir-label-sheet { position: absolute; left: 0; top: 0; width: 100%; border: none !important; box-shadow: none !important; }
}
`;

export function CartonLabelRoute() {
  const { receiptId = "", lpn = "" } = useParams();
  return <CartonLabelScreen key={`${receiptId}|${lpn}`} receiptId={receiptId} lpn={lpn} />;
}

/**
 * A printable license-plate label for one accepted carton (the usual reason:
 * an UnlabelledCarton that got an internal LPN). The service only returns
 * the label's DATA; the barcode is drawn here as an inline SVG (Code 128,
 * GS1-128 for an SSCC) and printed through the browser.
 */
export function CartonLabelScreen({ receiptId, lpn }: { receiptId: string; lpn: string }) {
  const label = useRequest(() => getCartonLabel(receiptId, lpn), `label|${receiptId}|${lpn}`);
  return (
    <Stack gap={5}>
      <style>{PRINT_CSS}</style>
      <div>
        <Link to={`../receipts/${encodeURIComponent(receiptId)}/discrepancies`}>← Carton discrepancies</Link>
      </div>
      <RequestView state={label} what="label" lead={(e) => problemLead(e.slug)}>
        {(l) => <Label label={l} />}
      </RequestView>
    </Stack>
  );
}

function Label({ label }: { label: CartonLabel }) {
  return (
    <>
      <PageHeader title="Carton label" subtitle="Check the preview, then print and stick it on the carton." />
      <div>
        <SubmitButton type="button" onClick={() => window.print()}>
          Print label
        </SubmitButton>
      </div>
      <Card title="Preview">
        <LabelSheet label={label} />
      </Card>
    </>
  );
}

function LabelSheet({ label }: { label: CartonLabel }) {
  return (
    <div
      className="ir-label-sheet"
      aria-label="Carton label"
      role="group"
      style={{
        width: 360,
        maxWidth: "100%",
        padding: 16,
        background: "#fff",
        color: "#000",
        border: "2px solid #000",
        display: "flex",
        flexDirection: "column",
        gap: 8,
        fontFamily: "var(--wh-font-mono)",
      }}
    >
      <div style={{ fontSize: 22, fontWeight: 700 }}>
        {label.sku ? `Case of ${eaches(label.units)}` : "Mixed carton"}
      </div>
      <div>{label.sku ? `SKU ${label.sku}` : "Several SKUs"}</div>
      <div>Quantity: {eaches(label.units)}</div>
      <Barcode value={barcodeData(label)} />
      <div style={{ textAlign: "center", fontSize: 16, letterSpacing: 1 }}>{label.humanReadable}</div>
      <div style={{ fontSize: 12 }}>Received {utcLabel(label.receivedAt)}</div>
    </div>
  );
}

function Barcode({ value }: { value: string }) {
  const code = code128Bars(value);
  if (!code) {
    return <div role="alert">This plate cannot be drawn as a Code 128 barcode; type it in by hand.</div>;
  }
  return (
    <svg
      data-testid="barcode"
      viewBox={`0 0 ${code.modules} 40`}
      preserveAspectRatio="none"
      style={{ width: "100%", height: 80 }}
      aria-label={`Code 128 barcode of ${value}`}
      role="img"
    >
      <rect width={code.modules} height={40} fill="#fff" />
      {code.bars.map((b) => (
        <rect key={b.x} x={b.x} y={0} width={b.width} height={40} fill="#000" />
      ))}
    </svg>
  );
}
