import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { RegisterAsnScreen } from "./RegisterAsnScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { asn } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter>
      <RegisterAsnScreen />
    </MemoryRouter>,
  );
}

async function fillHeader(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/^ASN number/), "ASN-1001");
  await user.type(screen.getByLabelText(/^Supplier reference/), "ACME");
}

async function fillLine(user: ReturnType<typeof userEvent.setup>, n: number, sku: string, qty: string) {
  await user.type(screen.getByLabelText(new RegExp(`^SKU \\(line ${n}\\)`)), sku);
  await user.type(screen.getByLabelText(new RegExp(`^Expected quantity \\(line ${n}\\)`)), qty);
}

describe("RegisterAsnScreen", () => {
  it("registers an ASN with 1..n line numbers, a UTC arrival and an Idempotency-Key", async () => {
    const api = mockApi({ "POST /asns": json(asn(), 201) });
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await user.type(screen.getByLabelText(/^Expected arrival \(UTC\)/), "2026-10-09T08:00");
    await fillLine(user, 1, "SKU-1", "40");
    await user.click(screen.getByRole("button", { name: "Add line" }));
    await fillLine(user, 2, "SKU-2", "5");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));

    expect(await screen.findByText(/Registered ASN-1001 with 2 lines \(version 1\)/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open ASN-1001" })).toBeInTheDocument();
    const [call] = api.to("POST /asns");
    expect(call.body).toEqual({
      asnNumber: "ASN-1001",
      supplierRef: "ACME",
      expectedArrival: "2026-10-09T08:00:00Z",
      lines: [
        { lineNo: 1, sku: "SKU-1", expectedQty: 40 },
        { lineNo: 2, sku: "SKU-2", expectedQty: 5 },
      ],
    });
    expect(call.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("omits expectedArrival when none was entered", async () => {
    const api = mockApi({ "POST /asns": json(asn(), 201) });
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await fillLine(user, 1, "SKU-1", "1");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    await screen.findByText(/Registered ASN-1001/);
    expect(api.to("POST /asns")[0].body).not.toHaveProperty("expectedArrival");
  });

  it("removes a line and renumbers the rest", async () => {
    const api = mockApi({ "POST /asns": json(asn(), 201) });
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await fillLine(user, 1, "SKU-1", "1");
    await user.click(screen.getByRole("button", { name: "Add line" }));
    await fillLine(user, 2, "SKU-2", "2");
    await user.click(screen.getByRole("button", { name: "Remove line 1" }));
    expect(screen.queryByLabelText(/^SKU \(line 2\)/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    await screen.findByText(/Registered ASN-1001/);
    expect((api.to("POST /asns")[0].body as { lines: unknown[] }).lines).toEqual([
      { lineNo: 1, sku: "SKU-2", expectedQty: 2 },
    ]);
  });

  it.each([
    ["no ASN number", async (u: ReturnType<typeof userEvent.setup>) => { await u.type(screen.getByLabelText(/^Supplier reference/), "A"); }, "Enter an ASN number."],
    ["a bad ASN number", async (u: ReturnType<typeof userEvent.setup>) => { await u.type(screen.getByLabelText(/^ASN number/), "ASN 1"); }, "An ASN number may only contain"],
    ["no supplier", async (u: ReturnType<typeof userEvent.setup>) => { await u.type(screen.getByLabelText(/^ASN number/), "ASN-1"); }, "Enter the supplier reference."],
  ])("refuses %s without calling the API", async (_name, fill, message) => {
    const api = mockApi({});
    const user = userEvent.setup();
    mount();
    await fill(user);
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(api.calls).toHaveLength(0);
  });

  it("refuses a non-whole or zero quantity and a duplicate SKU", async () => {
    const api = mockApi({});
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await fillLine(user, 1, "SKU-1", "0");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Line 1: the expected quantity must be a whole number");

    await user.clear(screen.getByLabelText(/^Expected quantity \(line 1\)/));
    await user.type(screen.getByLabelText(/^Expected quantity \(line 1\)/), "2.5");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("whole number");

    await user.clear(screen.getByLabelText(/^Expected quantity \(line 1\)/));
    await user.type(screen.getByLabelText(/^Expected quantity \(line 1\)/), "2");
    await user.click(screen.getByRole("button", { name: "Add line" }));
    await fillLine(user, 2, "SKU-1", "3");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Line 2: SKU SKU-1 is already on another line.");
    expect(api.calls).toHaveLength(0);
  });

  it("shows the RFC 7807 problem when the ASN already exists", async () => {
    mockApi({
      "POST /asns": problem(409, "asn-already-exists", "ASN already exists", "an asn with this number is already registered"),
    });
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await fillLine(user, 1, "SKU-1", "1");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("ASN already exists");
    expect(alert).toHaveTextContent("HTTP 409");
    expect(alert).toHaveTextContent("an asn with this number is already registered");
  });

  it("shows an unknown SKU (422) as a problem too", async () => {
    mockApi({ "POST /asns": problem(422, "unknown-sku", "SKU is not a registered product", "sku SKU-TYPO is not known to product-master") });
    const user = userEvent.setup();
    mount();
    await fillHeader(user);
    await fillLine(user, 1, "SKU-TYPO", "1");
    await user.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("sku SKU-TYPO is not known to product-master");
  });
});
