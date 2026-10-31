import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/customer-payments-api",
    },
    {
      type: "category",
      label: "Authorizations",
      link: {
        type: "doc",
        id: "api-reference/rest/authorizations",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/authorize-payment",
          label: "Authorize the quote total of a checkout session",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-payment-authorizations",
          label: "List authorizations by order or checkout session",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-payment-authorization",
          label: "Get one authorization with its allocations",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/void-payment-authorization",
          label: "Void an uncaptured authorization",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Refunds",
      link: {
        type: "doc",
        id: "api-reference/rest/refunds",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/request-refund",
          label: "Request a manual refund (ops only)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-refunds",
          label: "List refunds of an order",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
