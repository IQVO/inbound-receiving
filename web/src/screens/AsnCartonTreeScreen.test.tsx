import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AsnCartonTreeRoute } from "./AsnCartonTreeScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { PALLET, SSCC_1, SSCC_2, asnCartons } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/asns/ASN-1001/cartons"]}>
      <Routes>
        <Route path="/">
          <Route path="asns/:asnNumber/cartons" element={<AsnCartonTreeRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("AsnCartonTreeScreen", () => {
  it("shows pallet -> carton -> contents, a case as 'Case of N eaches', and each status", async () => {
    mockApi({ "GET /asns/ASN-1001/cartons": json(asnCartons()) });
    mount();
    expect(await screen.findByRole("heading", { name: "ASN-1001 · carton tree" })).toBeInTheDocument();
    expect(screen.getByText(`Pallet ${PALLET}`)).toBeInTheDocument();
    expect(screen.getByText("Cartons not on a pallet")).toBeInTheDocument();
    expect(screen.getByText("1 Expected · 1 Accepted · 1 Missing")).toBeInTheDocument();
    expect(screen.getByText("53 eaches")).toBeInTheDocument();
    expect(screen.getByText("2 cartons · 29 eaches")).toBeInTheDocument();

    const palletRows = within(screen.getAllByRole("table")[0]).getAllByRole("row").slice(1);
    expect(palletRows).toHaveLength(2);
    expect(within(palletRows[0]).getByText(SSCC_1)).toBeInTheDocument();
    expect(within(palletRows[0]).getByText("Case of 24 eaches")).toBeInTheDocument();
    expect(within(palletRows[0]).getByText("SKU-1: 24 eaches")).toBeInTheDocument();
    expect(within(palletRows[0]).getByText("Expected")).toHaveAttribute("data-tone", "neutral");
    expect(within(palletRows[1]).getByText(SSCC_2)).toBeInTheDocument();
    expect(within(palletRows[1]).getByText("Mixed carton")).toBeInTheDocument();
    expect(within(palletRows[1]).getByText("SKU-2: 1 each")).toBeInTheDocument();
    expect(within(palletRows[1]).getByText("Accepted")).toHaveAttribute("data-tone", "success");
    expect(screen.getByText("Missing")).toHaveAttribute("data-tone", "danger");
    expect(screen.getByRole("link", { name: /ASN-1001/ })).toHaveAttribute("href", "/asns/ASN-1001");
  });

  it("explains a flat ASN instead of showing an empty tree", async () => {
    mockApi({ "GET /asns/ASN-1001/cartons": json({ asnNumber: "ASN-1001", cartons: [] }) });
    mount();
    expect(await screen.findByText(/announced no cartons \(a flat ASN\)/)).toBeInTheDocument();
  });

  it("shows what happened and what to do for a problem code", async () => {
    mockApi({ "GET /asns/ASN-1001/cartons": problem(404, "asn-not-found", "Not found", "no such ASN") });
    mount();
    expect(await screen.findByText(/There is no ASN with that number\. Check the number/)).toBeInTheDocument();
    expect(screen.getByText("no such ASN")).toBeInTheDocument();
  });
});
