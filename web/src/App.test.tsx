import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { RCPT_ID, asn, docks, receipt } from "./test/fixtures";

function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

/** Mounted the way the console mounts the remote: inside the host's own
 *  `<Route path="/inbound-receiving/*">` splat route. Relative links behave
 *  differently there than at the router root, which root-mounted tests cannot
 *  see. */
function mountUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/inbound-receiving/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const SECTIONS = [
  "/inbound-receiving/asns",
  "/inbound-receiving/asns/register",
  "/inbound-receiving/appointments",
  "/inbound-receiving/receipts",
];

function subNavHrefs(): string[] {
  const nav = screen.getByRole("navigation", { name: "Inbound receiving sections" });
  return within(nav)
    .getAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "");
}

function routes() {
  return mockApi({
    "GET /asns": json({ items: [asn()] }),
    "GET /asns/ASN-1001": json(asn()),
    "GET /docks": json(docks()),
    "GET /appointments": json({ items: [] }),
    "GET /receipts": json({ items: [receipt()] }),
    [`GET /receipts/${RCPT_ID}`]: json(receipt()),
  });
}

describe("App (the exposed ./App)", () => {
  it("redirects the index route to the ASN list and shows the sub-nav", async () => {
    routes();
    mount("/");
    expect(await screen.findByRole("heading", { name: "Advance ship notices" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Inbound receiving sections" });
    for (const label of ["ASNs", "Register ASN", "Appointments", "Receipts"]) expect(nav).toHaveTextContent(label);
  });

  it("routes relatively at the root: every section and detail route renders", async () => {
    routes();
    const cases: [string, string][] = [
      ["/asns/register", "Register ASN"],
      ["/appointments", "Dock appointments"],
      ["/receipts", "Receipts"],
      ["/asns/ASN-1001", "ASN-1001"],
      [`/receipts/${RCPT_ID}`, RCPT_ID],
    ];
    for (const [path, heading] of cases) {
      const { unmount } = mount(path);
      expect(await screen.findByRole("heading", { name: heading })).toBeInTheDocument();
      unmount();
    }
  });

  describe("mounted under the console's /inbound-receiving/* splat route", () => {
    it("redirects /inbound-receiving to /inbound-receiving/asns", async () => {
      routes();
      mountUnderHost("/inbound-receiving");
      expect(await screen.findByRole("heading", { name: "Advance ship notices" })).toBeInTheDocument();
    });

    it.each([
      "/inbound-receiving/asns",
      "/inbound-receiving/asns/register",
      "/inbound-receiving/appointments",
      "/inbound-receiving/receipts",
      "/inbound-receiving/asns/ASN-1001",
    ])("sub-nav links resolve against the mount point, not the current URL (%s)", async (start) => {
      routes();
      mountUnderHost(start);
      await screen.findByRole("navigation", { name: "Inbound receiving sections" });
      expect(subNavHrefs()).toEqual(SECTIONS);
    });

    it("marks only the current section active", async () => {
      routes();
      mountUnderHost("/inbound-receiving/appointments");
      const nav = await screen.findByRole("navigation", { name: "Inbound receiving sections" });
      for (const name of ["ASNs", "Register ASN", "Appointments", "Receipts"]) {
        const link = within(nav).getByRole("link", { name });
        if (name === "Appointments") expect(link).toHaveAttribute("aria-current", "page");
        else expect(link).not.toHaveAttribute("aria-current");
      }
    });

    it("links an ASN row to its page and back, under the prefix", async () => {
      routes();
      const user = userEvent.setup();
      mountUnderHost("/inbound-receiving/asns");
      const row = await screen.findByRole("link", { name: "ASN-1001" });
      expect(row).toHaveAttribute("href", "/inbound-receiving/asns/ASN-1001");

      await user.click(row);
      expect(await screen.findByRole("heading", { name: "ASN-1001" })).toBeInTheDocument();
      const back = screen.getByRole("link", { name: "← All ASNs" });
      expect(back).toHaveAttribute("href", "/inbound-receiving/asns");

      await user.click(back);
      expect(await screen.findByRole("heading", { name: "Advance ship notices" })).toBeInTheDocument();
    });

    it("navigates between all sections in any order, hop after hop", async () => {
      routes();
      const user = userEvent.setup();
      mountUnderHost("/inbound-receiving/receipts");
      expect(await screen.findByRole("heading", { name: "Receipts" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "Appointments" }));
      expect(await screen.findByRole("heading", { name: "Dock appointments" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "Register ASN" }));
      expect(await screen.findByRole("heading", { name: "Register ASN" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "ASNs" }));
      expect(await screen.findByRole("heading", { name: "Advance ship notices" })).toBeInTheDocument();
      expect(subNavHrefs()).toEqual(SECTIONS);
    });

    it("links a freshly registered ASN to its page under the prefix", async () => {
      mockApi({ "POST /asns": json(asn({ asnNumber: "ASN-9" }), 201) });
      const user = userEvent.setup();
      mountUnderHost("/inbound-receiving/asns/register");
      await user.type(screen.getByLabelText(/^ASN number/), "ASN-9");
      await user.type(screen.getByLabelText(/^Supplier reference/), "ACME");
      await user.type(screen.getByLabelText(/^SKU \(line 1\)/), "SKU-1");
      await user.type(screen.getByLabelText(/^Expected quantity \(line 1\)/), "4");
      await user.click(screen.getByRole("button", { name: "Register ASN" }));
      expect(await screen.findByRole("link", { name: "Open ASN-9" })).toHaveAttribute(
        "href",
        "/inbound-receiving/asns/ASN-9",
      );
    });
  });
});
