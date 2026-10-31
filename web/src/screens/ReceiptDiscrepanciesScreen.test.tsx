import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ReceiptDiscrepanciesRoute } from "./ReceiptDiscrepanciesScreen";
import { CARTON_DISCREPANCY_KINDS } from "../types";
import { DISCREPANCY_GUIDE } from "../cartons";
import { json, mockApi, problem } from "../test/fetchMock";
import { LPN, RCPT_ID, SSCC_1, closedReceipt, receipt, scanResult } from "../test/fixtures";

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

const asnCartonsCalls = (api: ReturnType<typeof mockApi>) => api.calls.filter((c) => c.path.startsWith("/asns"));

describe("ReceiptDiscrepanciesScreen", () => {
  it("explains all nine kinds, with what it means and what to do, even with no findings", async () => {
    mockApi({ [`GET /receipts/${RCPT_ID}`]: json(receipt()) });
    mount();
    expect(await screen.findByText(/No carton findings on this receipt/)).toBeInTheDocument();
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

  it("reads an older service's receipt, which has no cartonDiscrepancies, as no findings", async () => {
    const { cartonDiscrepancies: _none, ...old } = receipt();
    mockApi({ [`GET /receipts/${RCPT_ID}`]: json(old) });
    mount();
    expect(await screen.findByText(/No carton findings on this receipt/)).toBeInTheDocument();
  });

  it("lists the findings the receipt carries, in raise order, whoever raised them, with no other request", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(
        closedReceipt({
          cartonDiscrepancies: [
            { kind: "UnexpectedCarton", sscc: SSCC_1, at: "2026-10-09T08:20:00Z" },
            { kind: "CartonOverLimit", sscc: SSCC_1, expected: "grossWeightG:20000", actual: "grossWeightG:25000", at: "2026-10-09T08:21:00Z" },
            { kind: "MissingCarton", expected: "340123450000000031", at: "2026-10-09T09:30:00Z" },
          ],
        }),
      ),
    });
    mount();
    await screen.findAllByText("340123450000000031");
    const rows = within(screen.getAllByRole("table")[0]).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("UnexpectedCarton")).toBeInTheDocument();
    expect(within(rows[0]).getByText("2026-10-09 08:20Z")).toBeInTheDocument();
    expect(within(rows[1]).getByText("weight 25000 g")).toBeInTheDocument();
    expect(within(rows[1]).getByText("CartonOverLimit")).toHaveAttribute("data-tone", "warning");
    // a missing carton is named by the manifest SSCC the receipt carries in `expected`
    expect(within(rows[2]).getByText("MissingCarton")).toBeInTheDocument();
    expect(within(rows[2]).getByText("2026-10-09 09:30Z")).toBeInTheDocument();
    // the ASN carton tree is no longer a second source for this screen
    expect(asnCartonsCalls(api)).toHaveLength(0);
    // a closed receipt takes no more scans
    expect(screen.queryByRole("button", { name: "Scan" })).not.toBeInTheDocument();
  });

  it("counts each kind on the receipt in the guide", async () => {
    mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(
        receipt({
          cartonDiscrepancies: [
            { kind: "DuplicateSscc", sscc: SSCC_1, at: "2026-10-09T08:20:00Z" },
            { kind: "DuplicateSscc", sscc: SSCC_1, at: "2026-10-09T08:25:00Z" },
          ],
        }),
      ),
    });
    mount();
    await screen.findAllByText("DuplicateSscc");
    const guide = within(screen.getAllByRole("table")[1]).getAllByRole("row").slice(1);
    const duplicate = guide[CARTON_DISCREPANCY_KINDS.indexOf("DuplicateSscc")];
    expect(within(duplicate).getByText("2")).toBeInTheDocument();
  });

  it("sends a scan with its Idempotency-Key, then re-reads the receipt and lists what it raised, with expected/actual in eaches", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
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
    await screen.findByText(/No carton findings on this receipt/);
    // what the receipt answers once the scan is stored
    api.set(
      `GET /receipts/${RCPT_ID}`,
      json(
        receipt({
          version: 2,
          cartonDiscrepancies: [
            { kind: "MixedCartonContentMismatch", sscc: SSCC_1, expected: "SKU-1:20", actual: "SKU-1:18", at: "2026-10-09T08:30:00Z" },
            { kind: "CartonOverLimit", sscc: SSCC_1, expected: "grossWeightG:20000", actual: "grossWeightG:25000", at: "2026-10-09T08:30:00Z" },
          ],
        }),
      ),
    );
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.type(screen.getByLabelText("Counted contents"), "SKU-1:18");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));

    expect(await screen.findByText(/Accepted\. 2 finding\(s\) were raised/)).toBeInTheDocument();
    const [call] = api.to(`POST /receipts/${RCPT_ID}/scans`);
    expect(call.body).toEqual({ kind: "SSCC", id: SSCC_1, countedContents: [{ sku: "SKU-1", qty: 18 }] });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(api.to(`GET /receipts/${RCPT_ID}`)).toHaveLength(2);

    const rows = within(screen.getAllByRole("table")[0]).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("SKU-1: 20 eaches")).toBeInTheDocument();
    expect(within(rows[0]).getByText("SKU-1: 18 eaches")).toBeInTheDocument();
    expect(within(rows[0]).getByText("MixedCartonContentMismatch")).toHaveAttribute("data-tone", "danger");
    expect(within(rows[1]).getByText("weight 25000 g")).toBeInTheDocument();
    expect(within(rows[1]).getByText("CartonOverLimit")).toHaveAttribute("data-tone", "warning");
    // nothing is kept in the browser: the receipt is the record
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });

  it("links an unlabelled carton to its label by the plate the receipt carries, and keeps it after a reload", async () => {
    mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(
        receipt({ cartonDiscrepancies: [{ kind: "UnlabelledCarton", actual: LPN, at: "2026-10-09T08:30:00Z" }] }),
      ),
    });
    const first = mount();
    expect(await screen.findByRole("link", { name: "Print label" })).toHaveAttribute(
      "href",
      `/receipts/${RCPT_ID}/cartons/${LPN}/label`,
    );
    expect(screen.getAllByText(LPN).length).toBeGreaterThan(0);
    first.unmount();

    // a fresh visit (another browser, a cleared session) shows the same
    mount();
    expect(await screen.findByRole("link", { name: "Print label" })).toBeInTheDocument();
  });

  it("reports a refused scan as not accepted, and a problem with what to do", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
      [`POST /receipts/${RCPT_ID}/scans`]: json(
        scanResult({ accepted: false, discrepancies: [{ kind: "UnexpectedCarton", sscc: SSCC_1 }] }),
      ),
    });
    mount();
    await screen.findByText(/No carton findings on this receipt/);
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText(/Not accepted: nothing was booked\. 1 finding/)).toBeInTheDocument();

    api.set(`POST /receipts/${RCPT_ID}/scans`, problem(422, "scan-kind-mismatch", "Scan kind mismatch", "that is a case GTIN"));
    await userEvent.type(screen.getByLabelText(/^Barcode/), "1");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText(/different kind of barcode from the one chosen/)).toBeInTheDocument();
    expect(screen.getByText("that is a case GTIN")).toBeInTheDocument();
  });

  it("says so when the receipt cannot be read again after a scan", async () => {
    const api = mockApi({
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
      [`POST /receipts/${RCPT_ID}/scans`]: json(scanResult({ accepted: false, discrepancies: [{ kind: "UnexpectedCarton", sscc: SSCC_1 }] })),
    });
    mount();
    await screen.findByText(/No carton findings on this receipt/);
    api.set(`GET /receipts/${RCPT_ID}`, problem(500, "internal-error", "Internal error", "db down"));
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText(/could not be read again/)).toBeInTheDocument();
    expect(screen.getByText(/Not accepted: nothing was booked/)).toBeInTheDocument();
  });

  it("validates before sending", async () => {
    const api = mockApi({ [`GET /receipts/${RCPT_ID}`]: json(receipt()) });
    mount();
    await screen.findByText(/No carton findings on this receipt/);
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(screen.getByText("Scan or type the barcode digits.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^Barcode/), SSCC_1);
    await userEvent.type(screen.getByLabelText("Counted contents"), "SKU-1");
    await userEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(screen.getByText(/is not SKU:quantity/)).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
