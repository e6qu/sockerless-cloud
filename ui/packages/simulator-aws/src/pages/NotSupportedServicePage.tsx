import { useParams } from "react-router";
import { AwsEmptyState, AwsNotSupportedBadge, AwsPageHeader, findNavService } from "../console/index.js";

/**
 * The real AWS console lists every service in its catalog, whether or not
 * this simulator implements it. Following a service sockerless has not built
 * a page for lands here: an honest statement of the gap, named against the
 * same catalog the side navigation renders from, rather than a broken route
 * or a page that silently pretends to be the real thing.
 */
export function NotSupportedServicePage() {
  const { service = "" } = useParams();
  const entry = findNavService(service);
  const label = entry?.label ?? service;
  return (
    <>
      <AwsPageHeader title={label} actions={<AwsNotSupportedBadge serviceName={label} />} />
      <AwsEmptyState
        title="This service is not implemented by the Sockerless simulator."
        description={`${label} is part of the real AWS service catalog. Sockerless has not built a simulator or backend for it, so there is no page here to show — the navigation still lists it, honestly marked, alongside the services sockerless does implement.`}
      />
    </>
  );
}
