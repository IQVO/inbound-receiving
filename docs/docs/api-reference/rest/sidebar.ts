import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/inbound-receiving-api",
    },
    {
      type: "category",
      label: "ASNs",
      link: {
        type: "doc",
        id: "api-reference/rest/as-ns",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/register-asn",
          label: "Register an ASN",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-asns",
          label: "List ASNs",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-asn",
          label: "Get an ASN",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/cancel-asn",
          label: "Cancel an ASN",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Appointments",
      link: {
        type: "doc",
        id: "api-reference/rest/appointments",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/book-appointment",
          label: "Book a dock appointment",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-appointments",
          label: "List dock appointments",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-appointment",
          label: "Get a dock appointment",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/check-in-appointment",
          label: "Check a carrier in",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/cancel-appointment",
          label: "Cancel a dock appointment",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Receipts",
      link: {
        type: "doc",
        id: "api-reference/rest/receipts",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/open-receipt",
          label: "Open a receipt",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-receipts",
          label: "List receipts",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-receipt",
          label: "Get a receipt",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/receive-line",
          label: "Receive a quantity against a line",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/close-receipt",
          label: "Close a receipt",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Docks",
      link: {
        type: "doc",
        id: "api-reference/rest/docks",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-docks",
          label: "List the inbound dock doors",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Health",
      link: {
        type: "doc",
        id: "api-reference/rest/health",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/healthz",
          label: "Liveness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/readyz",
          label: "Readiness probe",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Reports",
      link: {
        type: "doc",
        id: "api-reference/rest/reports",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-receiving-performance",
          label: "Receiving performance and accuracy per day, and the state right now",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-reports-freshness",
          label: "How far the analytics projection is behind",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
