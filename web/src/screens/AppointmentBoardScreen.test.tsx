import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AppointmentBoardScreen } from "./AppointmentBoardScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { APPT_ID, appointment, docks } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter>
      <AppointmentBoardScreen />
    </MemoryRouter>,
  );
}

async function openDay(day = "2026-10-09") {
  fireEvent.change(screen.getByLabelText("Day (UTC)"), { target: { value: day } });
}

function board(extra: Record<string, ReturnType<typeof json>> = {}) {
  return mockApi({
    "GET /docks": json(docks()),
    "GET /appointments": json({
      items: [
        appointment(),
        appointment({
          appointmentId: "appt-b",
          doorCode: "WH1-DOCK-IO-02",
          carrier: "Beta Haulage",
          windowStart: "2026-10-09T11:00:00Z",
          windowEnd: "2026-10-09T12:00:00Z",
          asnNumbers: ["ASN-1001", "ASN-1002"],
          state: "CheckedIn",
        }),
        appointment({
          appointmentId: "appt-c",
          carrier: "Early Bird",
          windowStart: "2026-10-09T06:00:00Z",
          windowEnd: "2026-10-09T07:00:00Z",
          state: "Completed",
        }),
      ],
    }),
    ...extra,
  });
}

describe("AppointmentBoardScreen", () => {
  it("shows a loading state", () => {
    mockApi({ "GET /docks": pending(), "GET /appointments": pending() });
    mount();
    expect(screen.getByText("Loading appointments…")).toBeInTheDocument();
  });

  it("asks for the chosen UTC day's [from, to) window and lays appointments out per door, in time order", async () => {
    const api = board();
    mount();
    await openDay();
    await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
    await waitFor(() => expect(api.to("GET /appointments").at(-1)?.query.get("from")).toBe("2026-10-09T00:00:00Z"));
    const last = api.to("GET /appointments").at(-1)!;
    expect(last.query.get("to")).toBe("2026-10-10T00:00:00Z");

    const door1 = screen.getByRole("heading", { name: "WH1-DOCK-IN-01" }).closest("section")!;
    const items = within(door1).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("06:00–07:00Z");
    expect(items[0]).toHaveTextContent("Early Bird");
    expect(items[1]).toHaveTextContent("08:00–10:00Z");
    expect(items[1]).toHaveTextContent("ACME Freight");

    const door2 = screen.getByRole("heading", { name: "WH1-DOCK-IO-02" }).closest("section")!;
    expect(within(door2).getByText("CheckedIn")).toHaveAttribute("data-tone", "progress");
    expect(within(door2).getByRole("link", { name: "ASN-1002" })).toHaveAttribute("href", "/asns/ASN-1002");
  });

  it("shows a door with nothing booked, from the dock list", async () => {
    mockApi({ "GET /docks": json(docks()), "GET /appointments": json({ items: [] }) });
    mount();
    expect(await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" })).toBeInTheDocument();
    expect(screen.getAllByText("Nothing booked.")).toHaveLength(2);
  });

  it("offers check-in and cancel only on a Booked appointment", async () => {
    board();
    mount();
    await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
    expect(screen.getAllByRole("button", { name: /^Check in / })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: /^Cancel (ACME|Beta|Early)/ })).toHaveLength(1);
  });

  it("checks a carrier in (POST with an Idempotency-Key) and reloads the board", async () => {
    const api = board({
      [`POST /appointments/${APPT_ID}/check-in`]: json(appointment({ state: "CheckedIn", version: 2 })),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: /^Check in ACME Freight/ }));
    await waitFor(() => expect(api.to("GET /appointments").length).toBeGreaterThanOrEqual(2));
    const [call] = api.to(`POST /appointments/${APPT_ID}/check-in`);
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("cancels a Booked appointment", async () => {
    const api = board({
      [`POST /appointments/${APPT_ID}/cancel`]: json(appointment({ state: "Cancelled", version: 2 })),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: /^Cancel ACME Freight/ }));
    await waitFor(() => expect(api.to(`POST /appointments/${APPT_ID}/cancel`)).toHaveLength(1));
    await waitFor(() => expect(api.to("GET /appointments").length).toBeGreaterThanOrEqual(2));
  });

  it("shows the problem when a check-in is outside the allowed window", async () => {
    board({
      [`POST /appointments/${APPT_ID}/check-in`]: problem(
        409,
        "outside-check-in-window",
        "Check-in is outside the allowed window",
        "check-in is only allowed from 30 minutes before the window until it ends",
      ),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: /^Check in ACME Freight/ }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Check-in is outside the allowed window");
    expect(alert).toHaveTextContent("HTTP 409");
    // The board is still there.
    expect(screen.getByRole("heading", { name: "WH1-DOCK-IN-01" })).toBeInTheDocument();
  });

  it("surfaces a failed appointments request", async () => {
    mockApi({ "GET /docks": json(docks()), "GET /appointments": problem(400, "invalid-query", "Invalid query parameter", "from must be RFC 3339") });
    mount();
    expect(await screen.findByText("from must be RFC 3339")).toBeInTheDocument();
  });

  it("still shows the appointments when the dock list cannot be loaded", async () => {
    mockApi({
      "GET /docks": problem(500, "internal-error", "Internal server error", "an unexpected error occurred"),
      "GET /appointments": json({ items: [appointment()] }),
    });
    mount();
    expect(await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("The dock door list could not be loaded.");
  });

  it("flags a day that is not a date and sends no request for it", async () => {
    const api = board();
    mount();
    await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
    const before = api.to("GET /appointments").length;
    await openDay("");
    expect(await screen.findByText("Enter a valid date.")).toBeInTheDocument();
    expect(api.to("GET /appointments")).toHaveLength(before);
  });

  describe("booking", () => {
    async function fillBooking(user: ReturnType<typeof userEvent.setup>, start = "2026-10-09T08:00", end = "2026-10-09T10:00") {
      await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
      await user.selectOptions(screen.getByLabelText(/^Dock door/), "WH1-DOCK-IN-01");
      await user.type(screen.getByLabelText(/^Carrier/), "ACME Freight");
      fireEvent.change(screen.getByLabelText(/^Window start \(UTC\)/), { target: { value: start } });
      fireEvent.change(screen.getByLabelText(/^Window end \(UTC\)/), { target: { value: end } });
      await user.type(screen.getByLabelText(/^ASN numbers/), "ASN-1001, ASN-1002 ASN-1001");
    }

    it("books a window (UTC, deduplicated ASNs, Idempotency-Key) and reloads the board", async () => {
      const api = board({ "POST /appointments": json(appointment(), 201) });
      const user = userEvent.setup();
      mount();
      await fillBooking(user);
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      expect(await screen.findByText(/Booked WH1-DOCK-IN-01 for ACME Freight, 08:00–10:00Z/)).toBeInTheDocument();
      const [call] = api.to("POST /appointments");
      expect(call.body).toEqual({
        doorCode: "WH1-DOCK-IN-01",
        carrier: "ACME Freight",
        windowStart: "2026-10-09T08:00:00Z",
        windowEnd: "2026-10-09T10:00:00Z",
        asnNumbers: ["ASN-1001", "ASN-1002"],
      });
      expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
      await waitFor(() => expect(api.to("GET /appointments").length).toBeGreaterThanOrEqual(2));
    });

    it("takes any door code when the service runs the door list in permissive mode", async () => {
      const api = mockApi({
        "GET /docks": json(docks("permissive")),
        "GET /appointments": json({ items: [] }),
        "POST /appointments": json(appointment({ doorCode: "ANY-DOOR" }), 201),
      });
      const user = userEvent.setup();
      mount();
      await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
      await user.type(screen.getByLabelText(/^Dock door/), "ANY-DOOR");
      await user.type(screen.getByLabelText(/^Carrier/), "C");
      fireEvent.change(screen.getByLabelText(/^Window start \(UTC\)/), { target: { value: "2026-10-09T08:00" } });
      fireEvent.change(screen.getByLabelText(/^Window end \(UTC\)/), { target: { value: "2026-10-09T09:00" } });
      await user.type(screen.getByLabelText(/^ASN numbers/), "ASN-1001");
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      await screen.findByText(/Booked ANY-DOOR/);
      expect((api.to("POST /appointments")[0].body as { doorCode: string }).doorCode).toBe("ANY-DOOR");
    });

    it.each([
      ["a window longer than four hours", "2026-10-09T08:00", "2026-10-09T12:01", "A window lasts at most 4 hours."],
      ["a window that ends before it starts", "2026-10-09T10:00", "2026-10-09T08:00", "The window must end after it starts."],
      ["a missing end", "2026-10-09T08:00", "", "Enter the window start and end (UTC)."],
    ])("refuses %s without calling the API", async (_n, start, end, message) => {
      const api = board();
      const user = userEvent.setup();
      mount();
      await fillBooking(user, start, end);
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(message);
      expect(api.to("POST /appointments")).toHaveLength(0);
    });

    it("refuses a booking with no ASN and one with no carrier", async () => {
      const api = board();
      const user = userEvent.setup();
      mount();
      await screen.findByRole("heading", { name: "WH1-DOCK-IN-01" });
      await user.selectOptions(screen.getByLabelText(/^Dock door/), "WH1-DOCK-IN-01");
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("Enter the carrier.");
      await user.type(screen.getByLabelText(/^Carrier/), "ACME");
      fireEvent.change(screen.getByLabelText(/^Window start \(UTC\)/), { target: { value: "2026-10-09T08:00" } });
      fireEvent.change(screen.getByLabelText(/^Window end \(UTC\)/), { target: { value: "2026-10-09T09:00" } });
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("Enter at least one ASN number.");
      expect(api.to("POST /appointments")).toHaveLength(0);
    });

    it("shows the problem when the door is already booked in that window", async () => {
      board({
        "POST /appointments": problem(409, "door-window-overlap", "Door is already booked in an overlapping window", "door is already booked in an overlapping window"),
      });
      const user = userEvent.setup();
      mount();
      await fillBooking(user);
      await user.click(screen.getByRole("button", { name: "Book appointment" }));
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent("Door is already booked in an overlapping window");
      expect(alert).toHaveTextContent("HTTP 409");
    });
  });
});
