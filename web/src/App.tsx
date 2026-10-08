import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { AppointmentBoardScreen } from "./screens/AppointmentBoardScreen";
import { AsnDetailRoute } from "./screens/AsnDetailScreen";
import { AsnListScreen } from "./screens/AsnListScreen";
import { ReceiptListScreen } from "./screens/ReceiptListScreen";
import { ReceiptWorkbenchRoute } from "./screens/ReceiptWorkbenchScreen";
import { RegisterAsnScreen } from "./screens/RegisterAsnScreen";

const SUB_NAV = [
  { to: "asns", label: "ASNs", end: true },
  { to: "asns/register", label: "Register ASN", end: false },
  { to: "appointments", label: "Appointments", end: false },
  { to: "receipts", label: "Receipts", end: false },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/inbound-receiving/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7 uses
 *  the last path-contributing match's pathname, splat included), so from
 *  /inbound-receiving/appointments the link `receipts` would point at
 *  /inbound-receiving/appointments/receipts. A pathless layout route does not
 *  help (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the product-master remote;
 *  App.test.tsx mounts under the host's splat route to prove it.) */
function InboundLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Inbound receiving sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} end={item.end} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as inbound_mfe/App via Module Federation. Takes NO props: the
 *  console mounts it under `/inbound-receiving/*` and provides the
 *  BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`. Routes
 *  here are RELATIVE, so the component works identically under that prefix (in
 *  the shell) or at / (standalone dev, see main.tsx).
 *
 *   - (index): redirects to asns.
 *   - asns: ASN list with state filter and paging.
 *   - asns/register: register an ASN.
 *   - asns/:asnNumber: one ASN, its receipts, open a receipt, cancel it.
 *   - appointments: the dock board per door and day; book, check in, cancel.
 *   - receipts: receipt list.
 *   - receipts/:receiptId: the receipt workbench (receive lines, close). */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<InboundLayout />}>
        <Route index element={<Navigate to="asns" replace />} />
        <Route path="asns" element={<AsnListScreen />} />
        <Route path="asns/register" element={<RegisterAsnScreen />} />
        <Route path="asns/:asnNumber" element={<AsnDetailRoute />} />
        <Route path="appointments" element={<AppointmentBoardScreen />} />
        <Route path="receipts" element={<ReceiptListScreen />} />
        <Route path="receipts/:receiptId" element={<ReceiptWorkbenchRoute />} />
      </Route>
    </Routes>
  );
}
