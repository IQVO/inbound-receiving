import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ReceiptWorkbenchRoute } from "./ReceiptWorkbenchScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { RCPT_ID, closedReceipt, receipt } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={[`/receipts/${RCPT_ID}`]}>
      <Routes>
        <Route path="/">
          <Route path="receipts/:receiptId" element={<ReceiptWorkbenchRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const LINES_PATH = `POST /receipts/${RCPT_ID}/lines`;
const CLOSE_PATH = `POST /receipts/${RCPT_ID}/close`;
const GET_PATH = `GET /receipts/${RCPT_ID}`;

function linesTable() {
  return screen.getAllByRole("table")[0];
}

function rowTexts(table: HTMLElement) {
  return within(table)
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getAllByRole("cell").map((c) => c.textContent));
}

/** The receipt after `qty` units of `lineNo` were received as `condition`. */
function withReceived(lineNo: number, good: number, damaged: number, version = 2) {
  const r = receipt({ version });
  return {
    ...r,
    lines: r.lines.map((l) => (l.lineNo === lineNo ? { ...l, receivedGood: good, receivedDamaged: damaged } : l)),
  };
}

describe("ReceiptWorkbenchScreen", () => {
  it("shows a loading state", () => {
    mockApi({ [GET_PATH]: pending() });
    mount();
    expect(screen.getByText("Loading receipt…")).toBeInTheDocument();
  });

  it("shows an Open receipt: facts, expected vs counted lines, receive form and close", async () => {
    mockApi({ [GET_PATH]: json(receipt()) });
    mount();
    expect(await screen.findByRole("heading", { name: RCPT_ID })).toBeInTheDocument();
    expect(screen.getByText("Open")).toHaveAttribute("data-tone", "progress");
    expect(screen.getByRole("link", { name: "ASN-1001" })).toHaveAttribute("href", "/asns/ASN-1001");
    expect(screen.getByText("WH1-DOCK-IN-01")).toBeInTheDocument();
    expect(rowTexts(linesTable())).toEqual([
      ["1", "SKU-1", "40", "0", "0", "40"],
      ["2", "SKU-2", "5", "0", "0", "5"],
    ]);
    expect(screen.getByRole("form", { name: "Receive line" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close receipt" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Discrepancies if closed now" })).toBeInTheDocument();
    expect(screen.getByText(/No discrepancies/)).toBeInTheDocument();
  });

  it("receives a Good quantity, updates the counts from the response and sends an Idempotency-Key", async () => {
    const api = mockApi({
      [GET_PATH]: json(receipt()),
      [LINES_PATH]: json(withReceived(1, 30, 0), 201),
    });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: RCPT_ID });
    await user.selectOptions(screen.getByLabelText(/^Line/), "1");
    await user.type(screen.getByLabelText(/^Quantity/), "30");
    await user.click(screen.getByRole("button", { name: "Record" }));

    expect(await screen.findByText("Recorded 30 Good on line 1 (SKU-1).")).toBeInTheDocument();
    const [call] = api.to(LINES_PATH);
    expect(call.body).toEqual({ lineNo: 1, quantity: 30, condition: "Good" });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(rowTexts(linesTable())[0]).toEqual(["1", "SKU-1", "40", "30", "0", "10"]);
    expect(screen.getByText("Version").nextSibling).toHaveTextContent("2");
    // The form is ready for the next count; no refetch was needed.
    expect(screen.getByLabelText(/^Quantity/)).toHaveValue("");
    expect(api.to(GET_PATH)).toHaveLength(1);
  });

  it("receives a Damaged quantity", async () => {
    const api = mockApi({
      [GET_PATH]: json(receipt()),
      [LINES_PATH]: json(withReceived(2, 0, 2), 201),
    });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: RCPT_ID });
    await user.selectOptions(screen.getByLabelText(/^Line/), "2");
    await user.type(screen.getByLabelText(/^Quantity/), "2");
    await user.selectOptions(screen.getByLabelText(/^Condition/), "Damaged");
    await user.click(screen.getByRole("button", { name: "Record" }));
    await screen.findByText("Recorded 2 Damaged on line 2 (SKU-2).");
    expect(api.to(LINES_PATH)[0].body).toEqual({ lineNo: 2, quantity: 2, condition: "Damaged" });
    expect(rowTexts(linesTable())[1]).toEqual(["2", "SKU-2", "5", "0", "2", "3"]);
  });

  it("each receive submit gets its own Idempotency-Key", async () => {
    const api = mockApi({ [GET_PATH]: json(receipt()), [LINES_PATH]: json(withReceived(1, 1, 0), 201) });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: RCPT_ID });
    await user.selectOptions(screen.getByLabelText(/^Line/), "1");
    for (let i = 0; i < 2; i++) {
      await user.type(screen.getByLabelText(/^Quantity/), "1");
      await user.click(screen.getByRole("button", { name: "Record" }));
      await screen.findAllByText("Recorded 1 Good on line 1 (SKU-1).");
    }
    const keys = api.to(LINES_PATH).map((c) => c.headers["Idempotency-Key"]);
    expect(keys).toHaveLength(2);
    expect(keys[0]).not.toBe(keys[1]);
  });

  it.each([
    ["no line chosen", "", "5", "Choose the line you are counting."],
    ["a zero quantity", "1", "0", "The quantity must be a whole number from 1"],
    ["a fractional quantity", "1", "2.5", "The quantity must be a whole number from 1"],
    ["no quantity", "1", "", "The quantity must be a whole number from 1"],
  ])("refuses %s without calling the API", async (_n, line, qty, message) => {
    const api = mockApi({ [GET_PATH]: json(receipt()) });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: RCPT_ID });
    if (line) await user.selectOptions(screen.getByLabelText(/^Line/), line);
    if (qty) await user.type(screen.getByLabelText(/^Quantity/), qty);
    await user.click(screen.getByRole("button", { name: "Record" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(api.to(LINES_PATH)).toHaveLength(0);
  });

  it("shows the RFC 7807 problem when a line is not on the ASN", async () => {
    mockApi({
      [GET_PATH]: json(receipt()),
      [LINES_PATH]: problem(422, "line-not-on-asn", "Line is not on the ASN", "line is not on the asn"),
    });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: RCPT_ID });
    await user.selectOptions(screen.getByLabelText(/^Line/), "1");
    await user.type(screen.getByLabelText(/^Quantity/), "1");
    await user.click(screen.getByRole("button", { name: "Record" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Line is not on the ASN");
    expect(alert).toHaveTextContent("HTTP 422");
    expect(alert).toHaveTextContent("line is not on the asn");
    // The counts are unchanged.
    expect(rowTexts(linesTable())[0]).toEqual(["1", "SKU-1", "40", "0", "0", "40"]);
  });

  it("shows live discrepancies for an Open receipt as they stand", async () => {
    const live = {
      ...withReceived(2, 7, 0),
      discrepancies: [{ lineNo: 2, sku: "SKU-2", kind: "Over" as const, expectedQty: 5, receivedQty: 7, damagedQty: 0 }],
    };
    mockApi({ [GET_PATH]: json(live) });
    mount();
    const card = (await screen.findByRole("heading", { name: "Discrepancies if closed now" })).closest("section")!;
    expect(within(card).getByRole("status")).toHaveTextContent("1 Over");
    expect(within(card).getByText("Over")).toHaveAttribute("data-tone", "warning");
  });

  it("closes only after confirmation, then shows the final discrepancy summary", async () => {
    const preview = {
      ...receipt(),
      discrepancies: [
        { lineNo: 1, sku: "SKU-1", kind: "Short" as const, expectedQty: 40, receivedQty: 0, damagedQty: 0 },
        { lineNo: 2, sku: "SKU-2", kind: "Short" as const, expectedQty: 5, receivedQty: 0, damagedQty: 0 },
      ],
    };
    const api = mockApi({ [GET_PATH]: json(preview), [CLOSE_PATH]: json(closedReceipt()) });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Close receipt" }));
    // Asked first, with what closing will record in front of the operator; nothing sent yet.
    expect(screen.getByText(/cannot be undone/)).toHaveTextContent("It will record: 2 Short.");
    expect(api.to(CLOSE_PATH)).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Keep receiving" }));
    expect(screen.getByRole("button", { name: "Close receipt" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close receipt" }));
    await user.click(screen.getByRole("button", { name: "Confirm close" }));

    const card = (await screen.findByRole("heading", { name: "Discrepancy summary (final)" })).closest("section")!;
    expect(within(card).getByRole("status")).toHaveTextContent("1 Short · 1 Over · 1 Damaged");
    expect(rowTexts(within(card).getByRole("table"))).toEqual([
      ["1", "SKU-1", "Short", "40", "34", "4"],
      ["1", "SKU-1", "Damaged", "40", "34", "4"],
      ["2", "SKU-2", "Over", "5", "7", "0"],
    ]);
    expect(within(card).getByText("Damaged", { selector: "[data-status]" })).toHaveAttribute("data-tone", "danger");
    expect(screen.getByText("Closed", { selector: "[data-status]" })).toHaveAttribute("data-tone", "success");
    expect(screen.getByText(/This receipt was closed at 2026-10-09 09:30Z/)).toBeInTheDocument();
    // Closed: no way to receive or close again.
    expect(screen.queryByRole("form", { name: "Receive line" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close receipt" })).not.toBeInTheDocument();
    const [call] = api.to(CLOSE_PATH);
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("says so when closing would record no discrepancies", async () => {
    mockApi({ [GET_PATH]: json(receipt()) });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Close receipt" }));
    expect(screen.getByText(/cannot be undone/)).toHaveTextContent("No discrepancies will be recorded.");
  });

  it("shows the problem if the close is refused and keeps the workbench usable", async () => {
    mockApi({
      [GET_PATH]: json(receipt()),
      [CLOSE_PATH]: problem(409, "receipt-closed", "Receipt is closed", "receipt is closed"),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Close receipt" }));
    await user.click(screen.getByRole("button", { name: "Confirm close" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("receipt is closed");
    expect(screen.getByRole("button", { name: "Confirm close" })).toBeEnabled();
  });

  it("opens a Closed receipt read-only with its final discrepancies", async () => {
    mockApi({ [GET_PATH]: json(closedReceipt()) });
    mount();
    expect(await screen.findByRole("heading", { name: "Discrepancy summary (final)" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Receive line" })).not.toBeInTheDocument();
    expect(rowTexts(linesTable())[1]).toEqual(["2", "SKU-2", "5", "7", "0", "0"]);
  });

  it("surfaces a 404 for an unknown receipt", async () => {
    mockApi({ [GET_PATH]: problem(404, "receipt-not-found", "Receipt not found", "receipt not found") });
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent("Receipt not found");
  });
});
