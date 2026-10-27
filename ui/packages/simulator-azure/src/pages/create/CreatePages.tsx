import { useState } from "react";
import { Link as RouterLink, useNavigate, useParams } from "react-router";
import { Button, Input, Spinner, Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow, Text } from "@fluentui/react-components";
import { serviceById } from "../../catalog.js";
import { bladeForResource } from "../../services.js";
import { Blade, EmptyState, ErrorBar, formatTime, StatusBadge } from "../../portal/AzurePortal.js";
import { useDeployment } from "../../portal/deployments.js";
import { AzureIcon, ServiceTile } from "../../portal/icons.js";
import { resourceRoute } from "../../portal/routes.js";
import { CREATE_SPECS } from "./specs.js";

/** Create a resource: the services the console can create, each opening its
 *  create wizard. */
export function CreateResourcePage() {
  const [filter, setFilter] = useState("");
  const needle = filter.trim().toLowerCase();
  const offers = CREATE_SPECS.map((spec) => ({ spec, service: serviceById(spec.serviceId) })).filter(
    ({ service }) => !needle || service.label.toLowerCase().includes(needle) || service.description.toLowerCase().includes(needle),
  );
  return (
    <Blade title="Create a resource" trail={[{ label: "Home", to: "/ui/home" }, { label: "Create a resource" }]} testid="create-resource">
      <Input
        className="az-filter-text"
        value={filter}
        onChange={(_, data) => setFilter(data.value)}
        placeholder="Search services"
        aria-label="Search services"
        contentBefore={<AzureIcon name="search" size={14} />}
      />
      <ul className="az-offers">
        {offers.map(({ spec, service }) => (
          <li key={spec.id} className="az-offer">
            <ServiceTile icon={service.icon} color={service.color} size={40} />
            <div className="az-offer-text">
              <Text weight="semibold" block>
                {service.label}
              </Text>
              <Text size={200} block>
                {service.description}
              </Text>
              <RouterLink to={spec.route} aria-label={`Create ${service.label}`}>
                Create
              </RouterLink>
            </div>
          </li>
        ))}
      </ul>
    </Blade>
  );
}

function duration(start: number, end?: number): string {
  const seconds = Math.max(0, Math.round(((end ?? Date.now()) - start) / 1000));
  return seconds < 60 ? `${seconds} seconds` : `${Math.floor(seconds / 60)} minutes ${seconds % 60} seconds`;
}

/** The deployment page: in progress while its operations run, then complete
 *  or failed, with each resource operation and the status URL ARM named. */
export function DeploymentPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const deployment = useDeployment(name);
  if (!deployment) {
    return (
      <Blade title={name} subtitle="Deployment" trail={[{ label: "Home", to: "/ui/home" }, { label: name }]}>
        <EmptyState title="This deployment ran in another browser session." description="Deployments are followed in the session that started them." />
      </Blade>
    );
  }
  const resourceBlade = bladeForResource(deployment.operations.at(-1)?.resourceType ?? "");
  const headline =
    deployment.status === "InProgress" ? "Deployment is in progress" : deployment.status === "Succeeded" ? "Your deployment is complete" : "Your deployment failed";
  return (
    <Blade
      title={`${name} | Overview`}
      subtitle="Deployment"
      icon={{ name: "rocket", color: "#0f6cbd" }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: name }]}
      testid="deployment"
    >
      <div className="az-deployment">
        <div className="az-deployment-head" role="status" aria-live="polite">
          {deployment.status === "InProgress" ? (
            <Spinner size="small" aria-label={headline} />
          ) : (
            <AzureIcon name={deployment.status === "Succeeded" ? "status_success" : "status_error"} size={32} />
          )}
          <Text as="h2" size={600} weight="semibold" data-testid="deployment-headline">
            {headline}
          </Text>
        </div>
        <dl className="az-deployment-facts">
          <div>
            <dt>Deployment name</dt>
            <dd>{deployment.name}</dd>
          </div>
          <div>
            <dt>Subscription</dt>
            <dd>{deployment.subscriptionId}</dd>
          </div>
          <div>
            <dt>Resource group</dt>
            <dd>
              <RouterLink to={resourceRoute(`/subscriptions/${deployment.subscriptionId}/resourceGroups/${deployment.resourceGroup}`)}>{deployment.resourceGroup}</RouterLink>
            </dd>
          </div>
          <div>
            <dt>Start time</dt>
            <dd>{formatTime(deployment.startTime)}</dd>
          </div>
          <div>
            <dt>Duration</dt>
            <dd>{duration(deployment.startTime, deployment.endTime)}</dd>
          </div>
        </dl>
        {deployment.error ? <ErrorBar title="The deployment failed.">{deployment.error}</ErrorBar> : null}
        <Text as="h3" size={400} weight="semibold" block>
          Deployment details
        </Text>
        <Table aria-label="Deployment details" size="small" className="az-grid" data-testid="deployment-operations">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Resource</TableHeaderCell>
              <TableHeaderCell>Type</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
              <TableHeaderCell>Operation details</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {deployment.operations.map((operation) => (
              <TableRow key={operation.resourceId}>
                <TableCell>
                  {operation.status === "Succeeded" ? <RouterLink to={resourceRoute(operation.resourceId)}>{operation.resourceName}</RouterLink> : operation.resourceName}
                </TableCell>
                <TableCell>{operation.resourceType}</TableCell>
                <TableCell>
                  <StatusBadge status={operation.status === "InProgress" ? "Created" : operation.status === "Succeeded" ? "OK" : "Failed"} kind={operation.status === "InProgress" ? "warning" : operation.status === "Succeeded" ? "success" : "error"} />
                </TableCell>
                <TableCell>{operation.statusMessage ?? (operation.operationUrl ? `Polling ${operation.operationUrl}` : "Started")}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <div className="az-deployment-actions">
          <Button appearance="primary" disabled={deployment.status !== "Succeeded"} onClick={() => navigate(resourceRoute(deployment.resourceId))} data-testid="deployment-go-to-resource">
            {resourceBlade?.kind === "Resource group" ? "Go to resource group" : "Go to resource"}
          </Button>
        </div>
      </div>
    </Blade>
  );
}
