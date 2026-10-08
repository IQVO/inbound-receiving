import { describe, expect, it } from "vitest";
import { INBOUND_RECEIVING_API_BASE, resolveInboundReceivingApiBase } from "./config";

describe("resolveInboundReceivingApiBase", () => {
  it("builds the production API base from the runtime API origin", () => {
    expect(resolveInboundReceivingApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/inbound-receiving",
    );
  });

  it("normalizes trailing slashes on the runtime API origin", () => {
    expect(resolveInboundReceivingApiBase({ apiOrigin: "https://warehouse.example/" }, true)).toBe(
      "https://warehouse.example/api/inbound-receiving",
    );
    expect(resolveInboundReceivingApiBase({ apiOrigin: "https://warehouse.example///" }, true)).toBe(
      "https://warehouse.example/api/inbound-receiving",
    );
  });

  it("fails loudly when production runtime configuration has no API origin", () => {
    expect(() => resolveInboundReceivingApiBase({}, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
    expect(() => resolveInboundReceivingApiBase({ apiOrigin: "" }, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
  });

  it("falls back to the service's own dev port outside production", () => {
    expect(resolveInboundReceivingApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("still prefers a runtime origin over the dev fallback", () => {
    expect(resolveInboundReceivingApiBase({ apiOrigin: "http://localhost:8000" }, false)).toBe(
      "http://localhost:8000/api/inbound-receiving",
    );
  });
});

describe("INBOUND_RECEIVING_API_BASE", () => {
  it("loads without /config.json (window.__WAREHOUSE_CONFIG__ absent) in a non-production build", () => {
    expect(window.__WAREHOUSE_CONFIG__).toBeUndefined();
    expect(INBOUND_RECEIVING_API_BASE).toBe("http://localhost:8080");
  });
});
