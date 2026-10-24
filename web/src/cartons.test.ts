import { describe, expect, it } from "vitest";
import {
  CARTON_DISCREPANCY_KINDS,
} from "./types";
import type { AsnCartonStatus } from "./types";
import {
  DISCREPANCY_GUIDE,
  PROBLEM_GUIDE,
  cartonKindLabel,
  code128Bars,
  code128Values,
  describeValue,
  eaches,
  groupByPallet,
  parseSkuQty,
  problemLead,
  statusSummary,
} from "./cartons";

function carton(over: Partial<AsnCartonStatus> = {}): AsnCartonStatus {
  return {
    sscc: "340123450000000017",
    kind: "HOMOGENEOUS_CASE",
    contents: [{ lineNo: 1, sku: "SKU-1", gtin: "00000000000000", qty: 24 }],
    status: "Expected",
    ...over,
  };
}

describe("eaches / cartonKindLabel", () => {
  it("pluralises and always names the unit", () => {
    expect(eaches(1)).toBe("1 each");
    expect(eaches(24)).toBe("24 eaches");
  });
  it("shows a case as 'Case of N eaches' summed over its contents, and a mixed carton as such", () => {
    expect(cartonKindLabel("HOMOGENEOUS_CASE", carton().contents)).toBe("Case of 24 eaches");
    expect(cartonKindLabel("HOMOGENEOUS_CASE", [{ lineNo: 1, sku: "A", gtin: "x", qty: 1 }])).toBe("Case of 1 each");
    expect(cartonKindLabel("MIXED", carton().contents)).toBe("Mixed carton");
  });
});

describe("groupByPallet / statusSummary", () => {
  it("groups under the pallet SSCC in first-seen order, loose cartons last, with eaches totals", () => {
    const nodes = groupByPallet([
      carton({ sscc: "1" }),
      carton({ sscc: "2", parentSscc: "P2" }),
      carton({ sscc: "3", parentSscc: "P1" }),
      carton({ sscc: "4", parentSscc: "P2" }),
    ]);
    expect(nodes.map((n) => [n.sscc, n.cartons.map((c) => c.sscc), n.eaches])).toEqual([
      ["P2", ["2", "4"], 48],
      ["P1", ["3"], 24],
      [undefined, ["1"], 24],
    ]);
  });
  it("counts statuses, omitting zeros", () => {
    expect(statusSummary([carton(), carton({ status: "Missing" }), carton()])).toBe("2 Expected · 1 Missing");
    expect(statusSummary([])).toBe("");
  });
});

describe("guides", () => {
  it("covers all nine carton discrepancy kinds with what and action copy", () => {
    expect(CARTON_DISCREPANCY_KINDS).toHaveLength(9);
    for (const k of CARTON_DISCREPANCY_KINDS) {
      expect(DISCREPANCY_GUIDE[k].what.length).toBeGreaterThan(10);
      expect(DISCREPANCY_GUIDE[k].action.length).toBeGreaterThan(10);
    }
    expect(Object.keys(DISCREPANCY_GUIDE).sort()).toEqual([...CARTON_DISCREPANCY_KINDS].sort());
  });
  it("marks exactly UnlabelledCarton and CartonOverLimit as informational (ADR 0014)", () => {
    expect(CARTON_DISCREPANCY_KINDS.filter((k) => !DISCREPANCY_GUIDE[k].blocking).sort()).toEqual([
      "CartonOverLimit",
      "UnlabelledCarton",
    ]);
  });
  it("has what/action copy for every problem code, and none is empty", () => {
    for (const g of Object.values(PROBLEM_GUIDE)) {
      expect(g.what).not.toBe("");
      expect(g.action).not.toBe("");
    }
    expect(problemLead("carton-not-found")).toContain("Scan the carton again");
    expect(problemLead("not-a-code")).toBeUndefined();
  });
});

describe("describeValue / parseSkuQty", () => {
  it("reads sku:qty in eaches", () => {
    expect(parseSkuQty("SKU-1:20")).toEqual({ sku: "SKU-1", qty: 20 });
    expect(parseSkuQty("A:B:3")).toEqual({ sku: "A:B", qty: 3 });
    expect(parseSkuQty("SKU-1")).toBeNull();
    expect(parseSkuQty("SKU-1:-1")).toBeNull();
    expect(parseSkuQty(undefined)).toBeNull();
  });
  it("renders each kind's expected/actual", () => {
    expect(describeValue({ kind: "MixedCartonContentMismatch", expected: "SKU-1:20", actual: "SKU-1:1" }, "actual")).toBe(
      "SKU-1: 1 each",
    );
    expect(describeValue({ kind: "MixedCartonContentMismatch", expected: "garbled" }, "expected")).toBe("garbled");
    expect(
      describeValue({ kind: "CartonOverLimit", expected: "grossWeightG:20000,longestSideMm:1200" }, "expected"),
    ).toBe("weight 20000 g, longest side 1200 mm");
    expect(describeValue({ kind: "CartonOverLimit", expected: "other:1" }, "expected")).toBe("other:1");
    expect(describeValue({ kind: "UnexpectedCarton" }, "expected")).toBe("—");
    expect(describeValue({ kind: "CaseQtyMismatch", expected: "24", actual: "20" }, "actual")).toBe("Case of 20 eaches");
    expect(describeValue({ kind: "DuplicateSscc", actual: "x" }, "actual")).toBe("x");
  });
});

describe("code128", () => {
  it("encodes Code B with the standard checksum (known value: 'A' -> start B, 33, check 34)", () => {
    // start B (104) + 'A' (33*1) = 137 mod 103 = 34
    expect(code128Values("A")).toEqual([104, 33, 34, 106]);
  });
  it("encodes an 18-digit SSCC as GS1-128 (start C, FNC1, AI 00, digit pairs)", () => {
    const v = code128Values("340123450000000017");
    expect(v?.slice(0, 4)).toEqual([105, 102, 0, 34]);
    expect(v).toHaveLength(2 + 10 + 2);
    expect(v?.at(-1)).toBe(106);
  });
  it("refuses empty or non-ASCII text", () => {
    expect(code128Values("")).toBeNull();
    expect(code128Values("é")).toBeNull();
    expect(code128Bars("")).toBeNull();
  });
  it("draws every symbol in 11 modules (stop 13) between 10-module quiet zones", () => {
    const r = code128Bars("A");
    expect(r).not.toBeNull();
    // 3 symbols * 11 + stop 13 + 2 * 10 quiet
    expect(r?.modules).toBe(3 * 11 + 13 + 20);
    expect(r?.bars[0].x).toBe(10);
  });
});

describe("code128 symbol table", () => {
  it("draws every printable ASCII character as a symbol of exactly 11 modules", () => {
    // Every drawn symbol must be 11 modules wide: encode each value alone through a start-B frame.
    for (let v = 0; v < 95; v++) {
      const ch = String.fromCharCode(32 + v);
      const r = code128Bars(ch);
      expect(r?.modules).toBe(3 * 11 + 13 + 20);
    }
  });
});
