import type {
  AsnCartonStatus,
  CartonContent,
  CartonDiscrepancy,
  CartonDiscrepancyKind,
  CartonKind,
  CartonLabel,
} from "./types";

/** "1 each" / "24 eaches". Quantities in this remote are ALWAYS eaches. */
export function eaches(n: number): string {
  return `${n} ${n === 1 ? "each" : "eaches"}`;
}

/** What a carton is, in plain words; a case always shows its size in eaches. */
export function cartonKindLabel(kind: CartonKind, contents: readonly CartonContent[]): string {
  if (kind === "MIXED") return "Mixed carton";
  const total = contents.reduce((sum, c) => sum + c.qty, 0);
  return `Case of ${total} ${total === 1 ? "each" : "eaches"}`;
}

export interface PalletNode {
  /** Undefined for the group of cartons that are not on a pallet. */
  sscc?: string;
  cartons: AsnCartonStatus[];
  /** Eaches announced across the cartons. */
  eaches: number;
}

/** Groups cartons under their pallet SSCC (pallets in first-seen order, loose cartons last). */
export function groupByPallet(cartons: readonly AsnCartonStatus[]): PalletNode[] {
  const pallets = new Map<string, AsnCartonStatus[]>();
  const loose: AsnCartonStatus[] = [];
  for (const c of cartons) {
    if (!c.parentSscc) {
      loose.push(c);
      continue;
    }
    const list = pallets.get(c.parentSscc) ?? [];
    list.push(c);
    pallets.set(c.parentSscc, list);
  }
  const total = (list: AsnCartonStatus[]) => list.reduce((s, c) => s + c.contents.reduce((n, k) => n + k.qty, 0), 0);
  const nodes: PalletNode[] = [...pallets].map(([sscc, list]) => ({ sscc, cartons: list, eaches: total(list) }));
  if (loose.length > 0) nodes.push({ cartons: loose, eaches: total(loose) });
  return nodes;
}

/** "3 Expected · 1 Accepted · 1 Missing" over the cartons. */
export function statusSummary(cartons: readonly AsnCartonStatus[]): string {
  return (["Expected", "Accepted", "Missing"] as const)
    .map((s) => [s, cartons.filter((c) => c.status === s).length] as const)
    .filter(([, n]) => n > 0)
    .map(([s, n]) => `${n} ${s}`)
    .join(" · ");
}

export interface DiscrepancyGuide {
  title: string;
  /** What happened, in plain language. */
  what: string;
  /** What the receiving lead does about it. */
  action: string;
  /** Informational findings do not block; the others go to problem-solve (ADR 0014). */
  blocking: boolean;
}

export const DISCREPANCY_GUIDE: Record<CartonDiscrepancyKind, DiscrepancyGuide> = {
  UnexpectedCarton: {
    title: "Carton not on the ASN",
    what: "A carton label was scanned whose SSCC the supplier did not announce. Nothing was booked.",
    action:
      "Check the label against the paperwork. Set the carton aside for problem-solve; do not put it away until the supplier or buyer confirms it.",
    blocking: true,
  },
  MissingCarton: {
    title: "Announced carton never arrived",
    what: "The supplier announced this carton but it was never accepted before the receipt closed.",
    action: "Look on the dock and in the trailer once more, then raise a shortage claim with the supplier.",
    blocking: true,
  },
  DuplicateSscc: {
    title: "Carton scanned twice",
    what: "This SSCC was already received (or scanned twice in a row). The second scan booked nothing.",
    action:
      "If it is the same carton, carry on: nothing is double-counted. If you hold a second physical carton with the same label, set it aside for problem-solve.",
    blocking: true,
  },
  UnknownGtin: {
    title: "Product barcode not known",
    what: "The scanned GTIN is not in the product catalogue's packaging data, so the quantity cannot be worked out. Nothing was booked.",
    action: "Check the barcode was read correctly. If it was, ask the product-master owner to declare the pack level.",
    blocking: true,
  },
  CaseQtyMismatch: {
    title: "Case size does not match",
    what: "The quantity in eaches differs from the case size the catalogue declares, or from the count on the label.",
    action: "Count the eaches in the case. If the case is really different, set it aside for problem-solve rather than receiving it as a full case.",
    blocking: true,
  },
  MixedCaseDeclaredAsCasePack: {
    title: "Mixed contents in a single-SKU case",
    what: "The label shows contents that contradict the SKU the supplier announced for this case.",
    action: "Open the case and check what is inside. Do not receive it as a case of the announced SKU until that is confirmed.",
    blocking: true,
  },
  UnlabelledCarton: {
    title: "Carton has no readable label",
    what: "A case arrived without a readable SSCC against an ASN that announced cartons. It was accepted and given an internal plate (LPN).",
    action: "Print the carton label and stick it on the carton before it moves. Informational: nothing else is needed.",
    blocking: false,
  },
  CartonOverLimit: {
    title: "Carton above a handling limit",
    what: "The declared weight or longest side is above the site limit. The carton was still accepted: it is on the dock.",
    action: "Flag the carton for team lift or equipment handling. Informational: no receiving action is blocked.",
    blocking: false,
  },
  MixedCartonContentMismatch: {
    title: "Counted contents differ from the manifest",
    what: "You counted this mixed carton and a SKU's count in eaches differs from what the supplier announced. The counted quantity is what was received.",
    action: "Recount the SKU. If the count stands, the shortage or surplus goes to problem-solve with the supplier.",
    blocking: true,
  },
};

/** Splits "SKU-1:20" into its SKU and quantity in eaches; null when it is not that shape. */
export function parseSkuQty(text: string | undefined): { sku: string; qty: number } | null {
  if (!text) return null;
  const i = text.lastIndexOf(":");
  if (i <= 0) return null;
  const qty = Number(text.slice(i + 1));
  return Number.isSafeInteger(qty) && qty >= 0 ? { sku: text.slice(0, i), qty } : null;
}

/** The expected/actual of a finding as operator text; quantities always in eaches. */
export function describeValue(d: CartonDiscrepancy, side: "expected" | "actual"): string {
  const raw = d[side];
  if (raw === undefined || raw === "") return "—";
  if (d.kind === "MixedCartonContentMismatch") {
    const p = parseSkuQty(raw);
    return p ? `${p.sku}: ${eaches(p.qty)}` : raw;
  }
  if (d.kind === "CartonOverLimit") {
    return raw
      .split(",")
      .map((part) => {
        const [k, v] = part.split(":");
        if (k === "grossWeightG") return `weight ${v} g`;
        if (k === "longestSideMm") return `longest side ${v} mm`;
        return part;
      })
      .join(", ");
  }
  if (d.kind === "CaseQtyMismatch" && /^\d+$/.test(raw)) return `Case of ${eaches(Number(raw))}`;
  return raw;
}

/**
 * What to tell the operator for each problem code the carton endpoints
 * return (slug = last path segment of problem.type): what happened and
 * what to do. Unknown slugs fall back to the server's own title/detail.
 */
export const PROBLEM_GUIDE: Record<string, { what: string; action: string }> = {
  "carton-not-found": {
    what: "The receipt has not accepted a carton with that plate, or does not know it.",
    action: "Scan the carton again so it is accepted first, then open its label.",
  },
  "receipt-not-found": {
    what: "There is no receipt with that id.",
    action: "Go back to the receipts list and open it from there.",
  },
  "receipt-closed": {
    what: "This receipt is closed, so nothing more can be scanned into it.",
    action: "Ask the receiving lead to open a new receipt if goods still need receiving.",
  },
  "asn-not-found": {
    what: "There is no ASN with that number.",
    action: "Check the number, or open the ASN from the ASN list.",
  },
  "scan-kind-mismatch": {
    what: "What you scanned is a different kind of barcode from the one chosen.",
    action: "Pick the matching scan type (carton SSCC, case GTIN or each GTIN) and scan again. Nothing was booked.",
  },
  "invalid-scan": {
    what: "The scan could not be read.",
    action: "Scan the barcode again, or type its digits.",
  },
  "invalid-sscc": {
    what: "The SSCC is not 18 digits with a valid check digit.",
    action: "Re-scan the label or check the digits.",
  },
  "invalid-gtin": {
    what: "The GTIN is not 8, 12, 13 or 14 digits with a valid check digit.",
    action: "Re-scan the barcode or check the digits.",
  },
  "invalid-cases": {
    what: "The number of cases is not allowed (1 to 1000).",
    action: "Enter a whole number of cases between 1 and 1000.",
  },
  "invalid-counted-contents": {
    what: "The counted contents repeat a SKU or contain a negative count.",
    action: "List each SKU once, with its count in eaches (0 or more).",
  },
  "counted-contents-not-mixed": {
    what: "A count was given for a carton that holds a single SKU.",
    action: "Only count mixed cartons. Scan the case without a count.",
  },
  "counted-sku-unknown": {
    what: "A counted SKU is not on the carton's manifest and has no each barcode in the catalogue.",
    action: "Check the SKU spelling. If it is right, ask the product-master owner to declare its each barcode.",
  },
  "homogeneous-carton-mixed": {
    what: "A single-SKU case holds more than one SKU.",
    action: "Set the case aside for problem-solve.",
  },
  "carton-contents-mismatch": {
    what: "The carton's contents contradict its manifest.",
    action: "Open the carton, count its contents in eaches and set it aside for problem-solve.",
  },
  "unsupported-pack-level": {
    what: "That barcode is for a pack level that cannot be received by scan.",
    action: "Scan the case barcode or the carton label instead.",
  },
  "version-mismatch": {
    what: "Someone else changed this receipt while you were working.",
    action: "Reload the page and try again.",
  },
  "duplicate-sscc": {
    what: "This SSCC was already received.",
    action: "Carry on with the next carton; nothing is double-counted.",
  },
  "internal-error": {
    what: "The service failed unexpectedly.",
    action: "Try again in a moment. If it keeps failing, tell the receiving lead.",
  },
};

export function problemLead(slug: string): string | undefined {
  const g = PROBLEM_GUIDE[slug];
  return g ? `${g.what} ${g.action}` : undefined;
}

/* ---------------------------------------------------------------- Code 128 */

/** Code 128 bar/space widths for symbol values 0..105 plus stop (106), 11 modules each (13 for stop). */
const PATTERNS = [
  "212222","222122","222221","121223","121322","131222","122213","122312","132212","221213",
  "221312","231212","112232","122132","122231","113222","123122","123221","223211","221132",
  "221231","213212","223112","312131","311222","321122","321221","312212","322112","322211",
  "212123","212321","232121","111323","131123","131321","112313","132113","132311","211313",
  "231113","231311","112133","112331","132131","113123","113321","133121","313121","211331",
  "231131","213113","213311","213131","311123","311321","331121","312113","312311","332111",
  "314111","221411","431111","111224","111422","121124","121421","141122","141221","112214",
  "112412","122114","122411","142112","142211","241211","221114","413111","241112","134111",
  "111242","121142","121241","114212","124112","124211","411212","421112","421211","212141",
  "214121","412121","111143","111341","131141","114113","114311","411113","411311","113141",
  "114131","311141","411131","211412","211214","211232","2331112",
];

const START_B = 104;
const START_C = 105;
const FNC1 = 102;
const STOP = 106;

/** Symbol values of a Code 128 barcode for `text` (GS1-128 when it is an 18-digit SSCC), or null when not encodable. */
export function code128Values(text: string): number[] | null {
  if (text.length === 0) return null;
  let values: number[];
  if (/^\d{18}$/.test(text)) {
    // GS1-128: Start C, FNC1, AI 00, then the 18 digits, in digit pairs.
    values = [START_C, FNC1];
    const digits = `00${text}`;
    for (let i = 0; i < digits.length; i += 2) values.push(Number(digits.slice(i, i + 2)));
  } else {
    values = [START_B];
    for (const ch of text) {
      const code = ch.charCodeAt(0);
      if (code < 32 || code > 126) return null;
      values.push(code - 32);
    }
  }
  const checksum = values.reduce((sum, v, i) => sum + v * (i === 0 ? 1 : i), 0) % 103;
  return [...values, checksum, STOP];
}

export interface Bar {
  x: number;
  width: number;
}

/** The dark bars (in modules) of a Code 128 barcode, with a 10-module quiet zone either side; null when not encodable. */
export function code128Bars(text: string): { bars: Bar[]; modules: number } | null {
  const values = code128Values(text);
  if (!values) return null;
  const QUIET = 10;
  let x = QUIET;
  const bars: Bar[] = [];
  for (const v of values) {
    const widths = PATTERNS[v].split("").map(Number);
    widths.forEach((w, i) => {
      if (i % 2 === 0) bars.push({ x, width: w });
      x += w;
    });
  }
  return { bars, modules: x + QUIET };
}

/** What the barcode encodes: the SSCC digits (GS1-128) for "(00)<sscc>", the LPN otherwise. */
export function barcodeData(label: CartonLabel): string {
  const m = /^\(00\)(\d{18})$/.exec(label.humanReadable);
  return m ? m[1] : label.lpn;
}
