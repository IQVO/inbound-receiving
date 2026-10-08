import { describe, expect, it } from "vitest";
import {
  asnNumberProblem,
  dayRange,
  doorCodeProblem,
  parseAsnNumbers,
  remainingQty,
  skuProblem,
  summarize,
  todayUtc,
  utcInputToIso,
  utcLabel,
  utcTime,
  wholeNumber,
} from "./lib";
import { closedReceipt } from "./test/fixtures";

describe("wholeNumber", () => {
  it("accepts whole non-negative numbers only", () => {
    expect(wholeNumber("12")).toBe(12);
    expect(wholeNumber(" 7 ")).toBe(7);
    for (const bad of ["", "12.5", "1e3", "abc", "-1", "9007199254740993"]) expect(wholeNumber(bad)).toBeNull();
  });
});

describe("UTC time helpers", () => {
  it("reads a datetime-local value as UTC", () => {
    expect(utcInputToIso("2026-10-09T08:00")).toBe("2026-10-09T08:00:00Z");
    expect(utcInputToIso("2026-10-09T08:00:30")).toBe("2026-10-09T08:00:30Z");
    expect(utcInputToIso("2026-13-09T08:00")).toBeNull();
    expect(utcInputToIso("nope")).toBeNull();
  });

  it("labels instants in UTC", () => {
    expect(utcLabel("2026-10-09T08:05:00Z")).toBe("2026-10-09 08:05Z");
    expect(utcLabel("2026-10-09T05:05:00-03:00")).toBe("2026-10-09 08:05Z");
    expect(utcLabel(undefined)).toBe("—");
    expect(utcLabel("garbage")).toBe("garbage");
    expect(utcTime("2026-10-09T08:05:00Z")).toBe("08:05");
  });

  it("todayUtc uses the UTC date, not the local one", () => {
    expect(todayUtc(new Date("2026-10-09T23:59:00Z"))).toBe("2026-10-09");
    expect(todayUtc(new Date("2026-10-10T00:01:00Z"))).toBe("2026-10-10");
  });

  it("dayRange spans [00:00Z, next 00:00Z) and rejects non-dates", () => {
    expect(dayRange("2026-10-09")).toEqual({ from: "2026-10-09T00:00:00Z", to: "2026-10-10T00:00:00Z" });
    expect(dayRange("2026-12-31")).toEqual({ from: "2026-12-31T00:00:00Z", to: "2027-01-01T00:00:00Z" });
    expect(dayRange("2026-02-30")).toBeNull();
    expect(dayRange("")).toBeNull();
    expect(dayRange("09/10/2026")).toBeNull();
  });
});

describe("field rules mirror the API", () => {
  it("sku", () => {
    expect(skuProblem("SKU-1")).toBeNull();
    expect(skuProblem("")).not.toBeNull();
    expect(skuProblem("a b")).not.toBeNull();
    expect(skuProblem("a/b")).not.toBeNull();
    expect(skuProblem("x".repeat(65))).not.toBeNull();
  });

  it("asn number", () => {
    expect(asnNumberProblem("ASN-1001.a_b")).toBeNull();
    expect(asnNumberProblem("")).not.toBeNull();
    expect(asnNumberProblem("ASN 1")).not.toBeNull();
    expect(asnNumberProblem("x".repeat(65))).not.toBeNull();
  });

  it("door code", () => {
    expect(doorCodeProblem("WH1-DOCK-IN-01")).toBeNull();
    expect(doorCodeProblem("")).not.toBeNull();
    expect(doorCodeProblem("a/b")).not.toBeNull();
  });
});

describe("parseAsnNumbers", () => {
  it("splits on commas and whitespace and drops blanks", () => {
    expect(parseAsnNumbers("ASN-1, ASN-2  ASN-3,,\n")).toEqual(["ASN-1", "ASN-2", "ASN-3"]);
    expect(parseAsnNumbers("  ")).toEqual([]);
  });
});

describe("receipt maths", () => {
  it("remaining never goes negative on over-receipt", () => {
    expect(remainingQty(40, 30, 4)).toBe(6);
    expect(remainingQty(5, 7, 0)).toBe(0);
  });

  it("summarizes discrepancies one count per kind, in a fixed order", () => {
    expect(summarize([])).toBeNull();
    expect(summarize(closedReceipt().discrepancies)).toBe("1 Short · 1 Over · 1 Damaged");
  });
});
