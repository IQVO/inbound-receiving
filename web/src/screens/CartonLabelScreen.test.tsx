import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { CartonLabelRoute } from "./CartonLabelScreen";
import { barcodeData } from "../cartons";
import { json, mockApi, problem } from "../test/fetchMock";
import { LPN, RCPT_ID, SSCC_1, cartonLabel } from "../test/fixtures";

function mount(lpn = LPN) {
  return render(
    <MemoryRouter initialEntries={[`/receipts/${RCPT_ID}/cartons/${lpn}/label`]}>
      <Routes>
        <Route path="/">
          <Route path="receipts/:receiptId/cartons/:lpn/label" element={<CartonLabelRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("CartonLabelScreen", () => {
  it("draws the label from the endpoint's data: case size in eaches, plate text, Code 128 barcode", async () => {
    const api = mockApi({ [`GET /receipts/${RCPT_ID}/cartons/${LPN}/label`]: json(cartonLabel()) });
    mount();
    const sheet = await screen.findByRole("group", { name: "Carton label" });
    expect(sheet).toHaveTextContent("Case of 20 eaches");
    expect(sheet).toHaveTextContent("SKU SKU-1");
    expect(sheet).toHaveTextContent(LPN);
    expect(sheet).toHaveTextContent("Received 2026-10-09 08:10Z");
    const svg = screen.getByTestId("barcode");
    expect(svg).toHaveAttribute("aria-label", `Code 128 barcode of ${LPN}`);
    expect(svg.querySelectorAll("rect").length).toBeGreaterThan(20);
    expect(api.calls).toHaveLength(1);
  });

  it("labels a multi-SKU carton as mixed and encodes an SSCC as its digits (GS1-128)", async () => {
    mockApi({
      [`GET /receipts/${RCPT_ID}/cartons/${SSCC_1}/label`]: json(
        cartonLabel({ lpn: SSCC_1, humanReadable: `(00)${SSCC_1}`, sku: undefined, units: 5 }),
      ),
    });
    mount(SSCC_1);
    const sheet = await screen.findByRole("group", { name: "Carton label" });
    expect(sheet).toHaveTextContent("Mixed carton");
    expect(sheet).toHaveTextContent("Quantity: 5 eaches");
    expect(sheet).toHaveTextContent(`(00)${SSCC_1}`);
    expect(screen.getByTestId("barcode")).toHaveAttribute("aria-label", `Code 128 barcode of ${SSCC_1}`);
    expect(barcodeData(cartonLabel({ humanReadable: `(00)${SSCC_1}` }))).toBe(SSCC_1);
  });

  it("prints through the browser and ships a print stylesheet that shows only the label", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}/cartons/${LPN}/label`]: json(cartonLabel()) });
    const print = vi.spyOn(window, "print").mockImplementation(() => {});
    const { container } = mount();
    await userEvent.click(await screen.findByRole("button", { name: "Print label" }));
    expect(print).toHaveBeenCalledTimes(1);
    const css = container.querySelector("style")?.textContent ?? "";
    expect(css).toContain("@media print");
    expect(css).toContain("body * { visibility: hidden");
    expect(css).toContain(".ir-label-sheet, .ir-label-sheet * { visibility: visible");
  });

  it("says what to do when the carton is not accepted yet", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}/cartons/${LPN}/label`]: problem(404, "carton-not-found", "Not found", "carton not accepted") });
    mount();
    expect(await screen.findByText(/Scan the carton again so it is accepted first/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Print label" })).not.toBeInTheDocument();
  });

  it("falls back to typing the plate when it cannot be drawn", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}/cartons/${LPN}/label`]: json(cartonLabel({ lpn: "LPN-é", humanReadable: "LPN-é" })) });
    mount();
    expect(await screen.findByText(/cannot be drawn as a Code 128 barcode/)).toBeInTheDocument();
  });
});
