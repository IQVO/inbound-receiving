import { describe, expect, it } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AsnDetailRoute } from "./AsnDetailScreen";
import { ReceiptWorkbenchRoute } from "./ReceiptWorkbenchScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { APPT_ID, RCPT_ID, appointment, asn, receipt } from "../test/fixtures";

function mount(path = "/asns/ASN-1001") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/">
          <Route path="asns/:asnNumber" element={<AsnDetailRoute />} />
          <Route path="receipts/:receiptId" element={<ReceiptWorkbenchRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function baseRoutes(state: "Registered" | "Receiving" | "Closed" | "Cancelled" = "Registered") {
  return {
    "GET /asns/ASN-1001": json(asn({ state })),
    "GET /receipts": json({ items: [] }),
    "GET /appointments": json({ items: [] }),
  };
}

describe("AsnDetailScreen", () => {
  it("shows the ASN, its lines and the actions that apply to a Registered ASN", async () => {
    mockApi(baseRoutes());
    mount();
    expect(await screen.findByRole("heading", { name: "ASN-1001" })).toBeInTheDocument();
    expect(screen.getByText("Registered")).toHaveAttribute("data-tone", "neutral");
    expect(screen.getByText("2026-10-09 08:00Z")).toBeInTheDocument();
    const rows = within(screen.getAllByRole("table")[0]).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual(["1SKU-140", "2SKU-25"]);
    expect(screen.getByRole("button", { name: "Open receipt" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel ASN" })).toBeInTheDocument();
    expect(await screen.findByText("Nothing has been received against this ASN yet.")).toBeInTheDocument();
  });

  it("offers no actions on a terminal ASN, and no cancel once it is being received", async () => {
    mockApi(baseRoutes("Closed"));
    const { unmount } = mount();
    await screen.findByRole("heading", { name: "ASN-1001" });
    expect(screen.queryByRole("button", { name: "Open receipt" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel ASN" })).not.toBeInTheDocument();
    unmount();

    mockApi(baseRoutes("Receiving"));
    mount();
    await screen.findByRole("heading", { name: "ASN-1001" });
    expect(screen.getByRole("button", { name: "Open receipt" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel ASN" })).not.toBeInTheDocument();
  });

  it("lists the receipts counted against the ASN, linked to the workbench", async () => {
    mockApi({ ...baseRoutes("Receiving"), "GET /receipts": json({ items: [receipt()] }) });
    mount();
    const link = await screen.findByRole("link", { name: RCPT_ID });
    expect(link).toHaveAttribute("href", `/receipts/${RCPT_ID}`);
  });

  it("opens a walk-in receipt (no appointmentId) and lands on the workbench", async () => {
    const api = mockApi({
      ...baseRoutes(),
      "POST /receipts": json(receipt({ appointmentId: undefined, doorCode: undefined }), 201),
      [`GET /receipts/${RCPT_ID}`]: json(receipt({ appointmentId: undefined, doorCode: undefined })),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Open receipt" }));
    expect(await screen.findByRole("heading", { name: RCPT_ID })).toBeInTheDocument();
    const [call] = api.to("POST /receipts");
    expect(call.body).toEqual({ asnNumber: "ASN-1001" });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(screen.getByText("Walk-in delivery (no appointment)")).toBeInTheDocument();
  });

  it("opens a receipt from a checked-in appointment that covers the ASN", async () => {
    const api = mockApi({
      ...baseRoutes(),
      "GET /appointments": json({
        items: [
          appointment({ state: "CheckedIn" }),
          appointment({ appointmentId: "appt-other", asnNumbers: ["ASN-9999"], state: "CheckedIn", doorCode: "OTHER" }),
        ],
      }),
      "POST /receipts": json(receipt(), 201),
      [`GET /receipts/${RCPT_ID}`]: json(receipt()),
    });
    const user = userEvent.setup();
    mount();
    const select = await screen.findByLabelText("Appointment");
    await waitFor(() => expect(within(select).getAllByRole("option")).toHaveLength(2));
    // Only the appointment that covers this ASN is offered.
    expect(within(select).queryByText(/OTHER/)).not.toBeInTheDocument();
    await user.selectOptions(select, APPT_ID);
    await user.click(screen.getByRole("button", { name: "Open receipt" }));
    await screen.findByRole("heading", { name: RCPT_ID });
    expect(api.to("POST /receipts")[0].body).toEqual({ asnNumber: "ASN-1001", appointmentId: APPT_ID });
    expect(api.to("GET /appointments")[0].query.get("state")).toBe("CheckedIn");
  });

  it("shows the problem when a receipt cannot be opened", async () => {
    mockApi({
      ...baseRoutes(),
      "POST /receipts": problem(409, "receipt-already-open", "ASN already has an open receipt", "only one receipt may be open per asn"),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Open receipt" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("ASN already has an open receipt");
    expect(alert).toHaveTextContent("only one receipt may be open per asn");
  });

  it("cancels a Registered ASN with a reason and shows it Cancelled", async () => {
    const api = mockApi({
      ...baseRoutes(),
      "POST /asns/ASN-1001/cancel": json(asn({ state: "Cancelled", version: 2 })),
    });
    const user = userEvent.setup();
    mount();
    await user.type(await screen.findByLabelText(/^Reason/), "Supplier cancelled");
    api.set("GET /asns/ASN-1001", json(asn({ state: "Cancelled", version: 2 })));
    await user.click(screen.getByRole("button", { name: "Cancel ASN" }));
    expect(await screen.findByText("Cancelled")).toHaveAttribute("data-tone", "danger");
    const [call] = api.to("POST /asns/ASN-1001/cancel");
    expect(call.body).toEqual({ reason: "Supplier cancelled" });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(screen.queryByRole("button", { name: "Cancel ASN" })).not.toBeInTheDocument();
  });

  it("shows the problem when the ASN cannot be cancelled", async () => {
    mockApi({
      ...baseRoutes(),
      "POST /asns/ASN-1001/cancel": problem(409, "asn-in-progress", "ASN is being received", "asn is being received and cannot be cancelled"),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Cancel ASN" }));
    expect(await screen.findByText("asn is being received and cannot be cancelled")).toBeInTheDocument();
  });

  it("surfaces a 404 for an unknown ASN", async () => {
    mockApi({ "GET /asns/ASN-1001": problem(404, "asn-not-found", "ASN not found", "asn not found"), "GET /receipts": json({ items: [] }) });
    mount();
    expect(await screen.findByText("ASN not found", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("HTTP 404");
  });
});
