import { describe, expect, it } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AsnListScreen } from "./AsnListScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { asn } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter>
      <AsnListScreen />
    </MemoryRouter>,
  );
}

describe("AsnListScreen", () => {
  it("shows a loading state", () => {
    mockApi({ "GET /asns": pending() });
    mount();
    expect(screen.getByText("Loading ASNs…")).toBeInTheDocument();
  });

  it("lists ASNs with supplier, state, arrival, line count and version", async () => {
    const api = mockApi({
      "GET /asns": json({
        items: [asn(), asn({ asnNumber: "ASN-1002", state: "Receiving", expectedArrival: undefined, version: 3 })],
      }),
    });
    mount();
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);

    const first = within(rows[0]);
    expect(first.getByRole("link", { name: "ASN-1001" })).toHaveAttribute("href", "/asns/ASN-1001");
    expect(first.getByText("ACME")).toBeInTheDocument();
    expect(first.getByText("Registered")).toHaveAttribute("data-tone", "neutral");
    expect(first.getByText("2026-10-09 08:00Z")).toBeInTheDocument();
    expect(first.getByText("2")).toBeInTheDocument();

    const second = within(rows[1]);
    expect(second.getByText("Receiving")).toHaveAttribute("data-tone", "progress");
    expect(second.getByText("—")).toBeInTheDocument();

    expect(Object.fromEntries(api.to("GET /asns")[0].query)).toEqual({ limit: "25" });
  });

  it("applies the state and page-size filters", async () => {
    const api = mockApi({ "GET /asns": json({ items: [asn()] }) });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("table");
    await user.selectOptions(screen.getByLabelText("State"), "Cancelled");
    await user.selectOptions(screen.getByLabelText("Page size"), "100 per page");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.to("GET /asns")).toHaveLength(2));
    expect(Object.fromEntries(api.to("GET /asns")[1].query)).toEqual({ limit: "100", state: "Cancelled" });
  });

  it("pages forward with nextCursor and back to the cursors already visited", async () => {
    const api = mockApi({
      "GET /asns": (call) => {
        const c = call.query.get("cursor");
        if (!c) return json({ items: [asn()], nextCursor: "C2" });
        return json({ items: [asn({ asnNumber: "ASN-2000" })] });
      },
    });
    const user = userEvent.setup();
    mount();
    await screen.findByRole("link", { name: "ASN-1001" });
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Next page" }));
    expect(await screen.findByRole("link", { name: "ASN-2000" })).toBeInTheDocument();
    expect(screen.getByText("Page 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Previous page" }));
    expect(await screen.findByRole("link", { name: "ASN-1001" })).toBeInTheDocument();
    expect(api.to("GET /asns").map((c) => c.query.get("cursor"))).toEqual([null, "C2", null]);
  });

  it("shows an empty state", async () => {
    mockApi({ "GET /asns": json({ items: [] }) });
    mount();
    expect(await screen.findByText("No ASNs match these filters.")).toBeInTheDocument();
  });

  it("surfaces the RFC 7807 title and detail when the list fails", async () => {
    mockApi({ "GET /asns": problem(400, "invalid-query", "Invalid query parameter", "limit must be 1..500") });
    mount();
    expect(await screen.findByText("Invalid query parameter")).toBeInTheDocument();
    expect(screen.getByText("limit must be 1..500")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("HTTP 400");
  });
});
