import { describe, expect, it } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { ReceiptListScreen } from "./ReceiptListScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { RCPT_ID, closedReceipt, receipt } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter>
      <ReceiptListScreen />
    </MemoryRouter>,
  );
}

describe("ReceiptListScreen", () => {
  it("shows a loading state", () => {
    mockApi({ "GET /receipts": pending() });
    mount();
    expect(screen.getByText("Loading receipts…")).toBeInTheDocument();
  });

  it("lists receipts with ASN, state, door (or walk-in), times and discrepancy count", async () => {
    const api = mockApi({
      "GET /receipts": json({
        items: [receipt(), { ...closedReceipt(), receiptId: "rcpt-2", doorCode: undefined, appointmentId: undefined }],
      }),
    });
    mount();
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);

    const first = within(rows[0]);
    expect(first.getByRole("link", { name: RCPT_ID })).toHaveAttribute("href", `/receipts/${RCPT_ID}`);
    expect(first.getByRole("link", { name: "ASN-1001" })).toHaveAttribute("href", "/asns/ASN-1001");
    expect(first.getByText("Open")).toHaveAttribute("data-tone", "progress");
    expect(first.getByText("WH1-DOCK-IN-01")).toBeInTheDocument();
    expect(first.getByText("2026-10-09 08:05Z")).toBeInTheDocument();

    const second = within(rows[1]);
    expect(second.getByText("walk-in")).toBeInTheDocument();
    expect(second.getByText("2026-10-09 09:30Z")).toBeInTheDocument();
    expect(second.getByText("3")).toBeInTheDocument();

    expect(Object.fromEntries(api.to("GET /receipts")[0].query)).toEqual({ limit: "25" });
  });

  it("filters by ASN number and state, and starts again at the first page", async () => {
    const api = mockApi({
      "GET /receipts": (call) =>
        call.query.get("cursor") ? json({ items: [receipt({ receiptId: "rcpt-2" })] }) : json({ items: [receipt()], nextCursor: "C2" }),
    });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("link", { name: RCPT_ID });
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByRole("link", { name: "rcpt-2" });

    await user.type(screen.getByLabelText("ASN number"), " ASN-1001 ");
    await user.selectOptions(screen.getByLabelText("State"), "Closed");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(screen.getByText("Page 1")).toBeInTheDocument());
    const last = api.to("GET /receipts").at(-1)!;
    expect(Object.fromEntries(last.query)).toEqual({ limit: "25", asnNumber: "ASN-1001", state: "Closed" });
  });

  it("shows an empty state", async () => {
    mockApi({ "GET /receipts": json({ items: [] }) });
    mount();
    expect(await screen.findByText("No receipts match these filters.")).toBeInTheDocument();
  });

  it("surfaces the RFC 7807 problem when the list fails", async () => {
    mockApi({ "GET /receipts": problem(500, "internal-error", "Internal server error", "an unexpected error occurred") });
    mount();
    expect(await screen.findByText("Internal server error")).toBeInTheDocument();
    expect(screen.getByText("an unexpected error occurred")).toBeInTheDocument();
  });
});
