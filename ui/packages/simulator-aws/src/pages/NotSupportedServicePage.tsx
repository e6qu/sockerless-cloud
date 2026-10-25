import { useParams } from "react-router";
import Container from "@cloudscape-design/components/container";
import Link from "@cloudscape-design/components/link";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsEmptyState, AwsNotSupportedBadge, AwsPageHeader, findService } from "../console/index.js";

/**
 * A service AWS offers that this console has no pages for. The Services menu
 * still lists it under its real category; following it lands here, on a plain
 * statement of what the console does not cover, rather than on a broken route.
 */
export function NotSupportedServicePage() {
  const { service = "" } = useParams();
  const entry = findService(service);
  const name = entry?.name ?? service;
  return (
    <SpaceBetween size="l">
      <AwsPageHeader title={name} description={entry?.description} actions={<AwsNotSupportedBadge serviceName={name} />} />
      <Container>
        <AwsEmptyState
          title="This console has no page for this service"
          description={`${name} is part of the AWS service catalog, and this console does not include pages for it.`}
          action={
            entry ? (
              <Link href={entry.docs} external>
                {name} documentation
              </Link>
            ) : undefined
          }
        />
      </Container>
    </SpaceBetween>
  );
}
