import { useNavigate } from "react-router";
import { useQueries } from "@tanstack/react-query";
import Container from "@cloudscape-design/components/container";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Box from "@cloudscape-design/components/box";
import Link from "@cloudscape-design/components/link";
import { AwsErrorAlert, AwsPageHeader, AwsStatus } from "../console/index.js";
import { REGION } from "../console/federation.js";
import {
  fetchECSTasks,
  fetchLambdaFunctions,
  fetchECRRepos,
  fetchS3Buckets,
  fetchCWLogGroups,
} from "../api.js";

/**
 * The AWS console's service dashboard states where you are looking before it
 * states what is there: Region and service health, then counts that link
 * through to the resource that owns them. Each count comes from the same real
 * API its resource page reads, not a sockerless-invented summary.
 */
const SERVICES = [
  { label: "Tasks", to: "/ui/ecs", queryKey: ["ecs-tasks"], queryFn: fetchECSTasks },
  { label: "Functions", to: "/ui/lambda", queryKey: ["lambda-functions"], queryFn: fetchLambdaFunctions },
  { label: "Repositories", to: "/ui/ecr", queryKey: ["ecr-repos"], queryFn: fetchECRRepos },
  { label: "Buckets", to: "/ui/s3", queryKey: ["s3-buckets"], queryFn: fetchS3Buckets },
  { label: "Log groups", to: "/ui/logs", queryKey: ["cw-log-groups"], queryFn: fetchCWLogGroups },
] as const;

export function OverviewPage() {
  const navigate = useNavigate();
  const results = useQueries({
    queries: SERVICES.map((service) => ({ queryKey: service.queryKey, queryFn: service.queryFn })),
  });
  const failed = results.some((result) => result.isError);
  const loading = results.some((result) => result.isLoading);

  if (loading) {
    return (
      <>
        <AwsPageHeader title="Overview" />
        <Container>
          <Box textAlign="center" color="text-body-secondary">
            Loading service overview…
          </Box>
        </Container>
      </>
    );
  }

  // A failed read must not render an all-zeros board that reads as healthy.
  if (failed) {
    return (
      <>
        <AwsPageHeader title="Overview" />
        <AwsErrorAlert>
          <strong>Could not load the service overview.</strong>{" "}
          {String(results.find((result) => result.error)?.error)}
        </AwsErrorAlert>
      </>
    );
  }

  return (
    <>
      <AwsPageHeader title="Overview" description="Resources in this account and Region." />
      <Container>
        <ColumnLayout columns={4} variant="text-grid">
          <div>
            <Box variant="awsui-key-label">Region</Box>
            <div>{REGION}</div>
            <Box variant="awsui-key-label" margin={{ top: "s" }}>
              Service health
            </Box>
            <AwsStatus status="Operating normally" kind="success" />
          </div>
          {SERVICES.map((service, index) => (
            <div key={service.label}>
              <Box variant="awsui-key-label">{service.label}</Box>
              <Link
                href={service.to}
                fontSize="display-l"
                onFollow={(event) => {
                  event.preventDefault();
                  navigate(service.to);
                }}
              >
                {results[index].data?.length ?? 0}
              </Link>
            </div>
          ))}
        </ColumnLayout>
      </Container>
    </>
  );
}
