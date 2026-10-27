{{/*
Fully qualified name of the frontend Module Federation deployment/service.

The remote (web/, `customer_ordering_mfe`) is served by its own nginx pod and
reached through warehouse-infra's Nginx web gateway at /mfes/customer-ordering/.
It is deliberately a separate workload from the API: the gateway never routes
to it, and the api Service must never select it (component=frontend vs
component=api). Kept in its own helper file so the frontend component stays
self-contained (templates/frontend-*.yaml + this file + the frontend: values
block).
*/}}
{{- define "customer-ordering.frontendFullname" -}}
{{- include "customer-ordering.fullname" . }}-frontend
{{- end }}

{{/*
Fully qualified name of the MCP deployment/service/route (ADR 0005).
*/}}
{{- define "customer-ordering.mcpFullname" -}}
{{- include "customer-ordering.fullname" . }}-mcp
{{- end }}
