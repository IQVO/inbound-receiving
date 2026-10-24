import { beforeEach, describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ReceiptDiscrepanciesRoute } from "./ReceiptDiscrepanciesScreen";
import { CARTON_DISCREPANCY_KINDS } from "../types";
import { DISCREPANCY_GUIDE } from "../cartons";
import { json, mockApi, problem } from "../test/fetchMock";
import { LPN, RCPT_ID, SSCC_1, asnCartons, closedReceipt, receipt, scanResult } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={[`/receipts/${RCPT_ID}/discrepancies`]}>
      <Routes>
        <Route path="/">
          <Route path="receipts/:receiptId/discrepancies" element={<ReceiptDiscrepanciesRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const flatCartons = json({ asnNumber: "ASN-1001", cartons: [] });

beforeEach(() => sessionStorage.clear());

describe("ReceiptDiscrepanciesScreen", () => {
  it("explains all nine kinds, with what it means and what to do, even with no findings", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}`]: json(receipt()), "GET /asns/ASN-1001/cartons": flatCartons });
    mount();
    expect(await screen.findByText(/No carton findings to show/)).toBeInTheDocument();
    const guide = screen.getAllByRole("table")[0];
    const rows = within(guide).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(9);
    CARTON_DISCREPANCY_KINDS.forEach((k, i) => {
      expect(within(rows[i]).getByText(k)).toBeInTheDocument();
      expect(within(rows[i]).getByText(DISCREPANCY_GUIDE[k].action)).toBeInTheDocument();
    });
    expect(within(rows[6]).getByText("Informational")).toBeInTheDocument();
    expect(within(rows[0]).getByText("Goes to problem-solve")).toBeInTheDocument();
  });

  it("shows MissingCarton for announced cartons the ASN reports Missing", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}`]: json(closedReceipt()), "GET /asns/ASN-1001/cartons": json(asnCartons()) });
    mount();
    await screen.findByText("340123450000000037");
    const table = screen.getAllByRole("table")[0];
    const row = within(table).getAllByRole("row")[1];
    expect(within(row).getByText("MissingCarton")).toBeInTheDocument();
    expect(within(row).getByText("340123450000000037")).toBeInTheDocument();
    // a closed receipt takes no more scans
    expect(screen.queryByRole("button", { name: "Scan" })).not.toBeInTheDocument();
  });

  it("sends a scan with its Idempotency-Key and lists the findings it raised, with expected/actual in eaches", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
      "GET /asns/ASN-1001/cartons": flatCartons,
      [`POST /receipts/${RCPT_ID}/scans`]: json(
        scanResult({
          discrepancies: [
            { kind: "MixedCartonContentMismatch", sscc: SSCC_1, expected: "SKU-1:20", actual: "SKU-1:18" },
            { kind: "CartonOverLimit", sscc: SSCC_1, expected: "grossWeightG:20000", actual: "grossWeightG:25000" },
          ],
        }),
        201,
      ),
    });
    mount();
    await screen.findByText(/No carton findings to show/);
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.type(screen.getByLabelText("Counted contents"), "SKU-1:18");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));

    expect(await screen.findByText(/Accepted\. 2 finding\(s\) were raised/)).toBeInTheDocument();
    const [call] = api.to(`POST /receipts/${RCPT_ID}/scans`);
    expect(call.body).toEqual({ kind: "SSCC", id: SSCC_1, countedContents: [{ sku: "SKU-1", qty: 18 }] });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);

    const rows = within(screen.getAllByRole("table")[0]).getAllByRole("row").slice(1);
    expect(within(rows[0]).getByText("SKU-1: 20 eaches")).toBeInTheDocument();
    expect(within(rows[0]).getByText("SKU-1: 18 eaches")).toBeInTheDocument();
    expect(within(rows[0]).getByText("MixedCartonContentMismatch")).toHaveAttribute("data-tone", "danger");
    expect(within(rows[1]).getByText("weight 25000 g")).toBeInTheDocument();
    expect(within(rows[1]).getByText("CartonOverLimit")).toHaveAttribute("data-tone", "warning");
  });

  it("keeps findings for the session and links an unlabelled carton to its label", async () => {
    mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
      "GET /asns/ASN-1001/cartons": flatCartons,
      [`POST /receipts/${RCPT_ID}/scans`]: json(
        scanResult({
          kind: "CASE_GTIN",
          cartons: [{ lpn: LPN, kind: "HOMOGENEOUS_CASE", status: "Accepted", sealed: false, contents: [] }],
          discrepancies: [{ kind: "UnlabelledCarton" }],
        }),
        201,
      ),
    });
    const first = mount();
    await screen.findByText(/No carton findings to show/);
    await userEvent.selectOptions(screen.getByLabelText(/Scan type/), "CASE_GTIN");
    await userEvent.type(screen.getByLabelText(/^Barcode/), "00614141000036");
    await userEvent.type(screen.getByLabelText("Cases"), "2");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByRole("link", { name: "Print label" })).toHaveAttribute(
      "href",
      `/receipts/${RCPT_ID}/cartons/${LPN}/label`,
    );
    first.unmount();

    mount();
    expect(await screen.findByRole("link", { name: "Print label" })).toBeInTheDocument();
  });

  it("reports a refused scan as not accepted, and a problem with what to do", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
      "GET /asns/ASN-1001/cartons": flatCartons,
      [`POST /receipts/${RCPT_ID}/scans`]: json(
        scanResult({ accepted: false, discrepancies: [{ kind: "UnexpectedCarton", sscc: SSCC_1 }] }),
      ),
    });
    mount();
    await screen.findByText(/No carton findings to show/);
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText(/Not accepted: nothing was booked\. 1 finding/)).toBeInTheDocument();

    api.set(`POST /receipts/${RCPT_ID}/scans`, problem(422, "scan-kind-mismatch", "Scan kind mismatch", "that is a case GTIN"));
    await userEvent.type(screen.getByLabelText(/^Barcode/), "1");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText(/different kind of barcode from the one chosen/)).toBeInTheDocument();
    expect(screen.getByText("that is a case GTIN")).toBeInTheDocument();
  });

  it("validates before sending", async () => {
    const api = mockApi({ [`GET /receipts/${RCPT_ID}`]: json(receipt()), "GET /asns/ASN-1001/cartons": flatCartons });
    mount();
    await screen.findByText(/No carton findings to show/);
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(screen.getByText("Scan or type the barcode digits.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.type(screen.getByLabelText("Counted contents"), "SKU-1");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(screen.getByText(/is not SKU:quantity/)).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
