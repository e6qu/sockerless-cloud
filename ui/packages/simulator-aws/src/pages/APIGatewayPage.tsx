import { AwsButton, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { formatEpoch, formatTimestamp } from "../console/format.js";
import {
  fetchAPIGatewayRestApis,
  fetchAPIGatewayV2Apis,
  type APIGatewayRestApi,
  type APIGatewayV2Api,
} from "../api.js";

// Amazon API Gateway — APIs. The v1 (REST API) and v2 (HTTP and WebSocket API)
// surfaces are two separate AWS APIs, and the real console lists both together:
// GET /restapis and GET /v2/apis.

const restColumns: AwsColumn<APIGatewayRestApi>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "API ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  {
    id: "endpointTypes",
    header: "Endpoint type",
    cell: (row) => row.endpointTypes.join(", ") || "–",
    value: (row) => row.endpointTypes.join(", "),
  },
  {
    id: "createdDate",
    header: "Created",
    cell: (row) => formatEpoch(row.createdDate),
    value: (row) => String(row.createdDate),
  },
];

const v2Columns: AwsColumn<APIGatewayV2Api>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "apiId", header: "API ID", cell: (row) => row.apiId, value: (row) => row.apiId },
  { id: "protocolType", header: "Protocol", cell: (row) => row.protocolType, value: (row) => row.protocolType },
  { id: "apiEndpoint", header: "API endpoint", cell: (row) => row.apiEndpoint || "–", value: (row) => row.apiEndpoint },
  {
    id: "createdDate",
    header: "Created",
    cell: (row) => formatTimestamp(row.createdDate),
    value: (row) => row.createdDate,
  },
];

export function APIGatewayPage() {
  return (
    <>
      <AwsResourceTable<APIGatewayRestApi>
        title="REST APIs"
        description="Amazon API Gateway REST APIs in this account and Region."
        columns={restColumns}
        queryKey={["apigateway-rest-apis"]}
        queryFn={fetchAPIGatewayRestApis}
        filterPlaceholder="Find REST APIs"
        emptyTitle="No REST APIs"
        emptyDescription="No REST APIs exist in this account and Region."
        rowKey={(row) => row.id}
        tableTestId="apigateway-rest-apis-table"
        errorTestId="apigateway-rest-apis-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      <AwsResourceTable<APIGatewayV2Api>
        title="HTTP and WebSocket APIs"
        headingVariant="h2"
        description="Amazon API Gateway v2 APIs in this account and Region."
        columns={v2Columns}
        queryKey={["apigateway-v2-apis"]}
        queryFn={fetchAPIGatewayV2Apis}
        filterPlaceholder="Find APIs"
        emptyTitle="No HTTP or WebSocket APIs"
        emptyDescription="No API Gateway v2 APIs exist in this account and Region."
        rowKey={(row) => row.apiId}
        tableTestId="apigateway-v2-apis-table"
        errorTestId="apigateway-v2-apis-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
    </>
  );
}
