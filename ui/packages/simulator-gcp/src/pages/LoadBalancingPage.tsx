import { useQuery } from "@tanstack/react-query";
import { GcpPageHeader } from "../console/GcpConsole.js";
import { GcpTabs } from "../console/GcpTabs.js";
import { shortName, formatTimestamp } from "../console/format.js";
import { SubResourceTable } from "./SubResourceTable.js";
import {
  fetchBackendServices,
  fetchForwardingRules,
  fetchHealthChecks,
  fetchTargetHttpProxies,
  fetchUrlMaps,
  type ComputeBackendService,
  type ComputeForwardingRule,
  type ComputeHealthCheck,
  type ComputeTargetHttpProxy,
  type ComputeUrlMap,
} from "../api.js";
import { useProject } from "../console/project.js";

const lastSegment = (value: string | undefined): string => (value ? shortName(value) : "—");

// Cloud Load Balancing is assembled from five separate Compute Engine
// collections rather than one "load balancer" resource, and the real console's
// Load balancing page shows them as its own tab strip (Frontends, Backends,
// Health checks). Each tab reads its own real global list method.
export function LoadBalancingPage() {
  const { project } = useProject();
  const backendServices = useQuery({
    queryKey: ["lb-backend-services", project],
    queryFn: () => fetchBackendServices(project),
  });
  const urlMaps = useQuery({ queryKey: ["lb-url-maps", project], queryFn: () => fetchUrlMaps(project) });
  const forwardingRules = useQuery({
    queryKey: ["lb-forwarding-rules", project],
    queryFn: () => fetchForwardingRules(project),
  });
  const healthChecks = useQuery({ queryKey: ["lb-health-checks", project], queryFn: () => fetchHealthChecks(project) });
  const proxies = useQuery({ queryKey: ["lb-target-proxies", project], queryFn: () => fetchTargetHttpProxies(project) });

  const refreshing =
    backendServices.isFetching ||
    urlMaps.isFetching ||
    forwardingRules.isFetching ||
    healthChecks.isFetching ||
    proxies.isFetching;

  return (
    <>
      <GcpPageHeader
        title="Load balancing"
        description="Cloud Load Balancing distributes traffic across your backends, using forwarding rules, URL maps, target proxies, backend services and health checks."
        onRefresh={() => {
          void backendServices.refetch();
          void urlMaps.refetch();
          void forwardingRules.refetch();
          void healthChecks.refetch();
          void proxies.refetch();
        }}
        refreshing={refreshing}
      />
      <GcpTabs
        label="Load balancing"
        tabs={[
          {
            id: "frontends",
            label: "Frontends",
            content: (
              <SubResourceTable<ComputeForwardingRule>
                query={forwardingRules}
                testId="lb-forwarding-rules-table"
                noun="forwarding rules"
                emptyHeadline="This project has no forwarding rules"
                emptyDescription="A forwarding rule sends traffic arriving at an IP address and port to a target proxy."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Name", cell: (row) => row.name },
                  { header: "IP address", cell: (row) => row.IPAddress ?? "—" },
                  { header: "Protocol", cell: (row) => row.IPProtocol ?? "—" },
                  { header: "Port range", cell: (row) => row.portRange ?? "—" },
                  { header: "Target", cell: (row) => lastSegment(row.target) },
                ]}
              />
            ),
          },
          {
            id: "proxies",
            label: "Target proxies",
            content: (
              <SubResourceTable<ComputeTargetHttpProxy>
                query={proxies}
                testId="lb-target-proxies-table"
                noun="target proxies"
                emptyHeadline="This project has no target HTTP proxies"
                emptyDescription="A target proxy terminates the client connection and consults a URL map."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Name", cell: (row) => row.name },
                  { header: "URL map", cell: (row) => lastSegment(row.urlMap) },
                  { header: "Created", cell: (row) => formatTimestamp(row.creationTimestamp ?? "") },
                ]}
              />
            ),
          },
          {
            id: "urlmaps",
            label: "URL maps",
            content: (
              <SubResourceTable<ComputeUrlMap>
                query={urlMaps}
                testId="lb-url-maps-table"
                noun="URL maps"
                emptyHeadline="This project has no URL maps"
                emptyDescription="A URL map routes each request to a backend service by host and path."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Name", cell: (row) => row.name },
                  { header: "Default service", cell: (row) => lastSegment(row.defaultService) },
                  { header: "Created", cell: (row) => formatTimestamp(row.creationTimestamp ?? "") },
                ]}
              />
            ),
          },
          {
            id: "backends",
            label: "Backends",
            content: (
              <SubResourceTable<ComputeBackendService>
                query={backendServices}
                testId="lb-backend-services-table"
                noun="backend services"
                emptyHeadline="This project has no backend services"
                emptyDescription="A backend service defines how traffic is distributed across a group of backends."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Name", cell: (row) => row.name },
                  { header: "Protocol", cell: (row) => row.protocol ?? "—" },
                  { header: "Scheme", cell: (row) => row.loadBalancingScheme ?? "—" },
                  { header: "Timeout", cell: (row) => (row.timeoutSec ? `${row.timeoutSec}s` : "—") },
                  {
                    header: "Health checks",
                    cell: (row) => (row.healthChecks ?? []).map((check) => shortName(check)).join(", ") || "—",
                  },
                ]}
              />
            ),
          },
          {
            id: "healthchecks",
            label: "Health checks",
            content: (
              <SubResourceTable<ComputeHealthCheck>
                query={healthChecks}
                testId="lb-health-checks-table"
                noun="health checks"
                emptyHeadline="This project has no health checks"
                emptyDescription="A health check probes your backends and removes unhealthy ones from rotation."
                rowKey={(row) => row.name}
                columns={[
                  { header: "Name", cell: (row) => row.name },
                  { header: "Protocol", cell: (row) => row.type ?? "—" },
                  { header: "Check interval", cell: (row) => (row.checkIntervalSec ? `${row.checkIntervalSec}s` : "—") },
                  { header: "Timeout", cell: (row) => (row.timeoutSec ? `${row.timeoutSec}s` : "—") },
                ]}
              />
            ),
          },
        ]}
      />
    </>
  );
}
