import { useState, type ReactNode } from "react";
import { BrowserRouter, Routes, useLocation } from "react-router";
import { ErrorBoundary, OperatorAccount } from "@sockerless/ui-core/components";
import AppLayout from "@cloudscape-design/components/app-layout";
import { AwsBreadcrumbs, AwsHeader, AwsSideNavigation } from "./AwsConsole.js";
import { REGION } from "./federation.js";
import { findNavService, NAV_GROUPS } from "./serviceCatalog.js";

// Every service route's breadcrumb label, keyed by its own route. The labels
// are the services' real, fully-qualified AWS names, matching the side
// navigation's own entries in serviceCatalog.ts.
const CRUMBS: Record<string, string> = {
  "/ui/": "Overview",
  "/ui/ecs": "Elastic Container Service",
  "/ui/lambda": "Lambda",
  "/ui/ecr": "Elastic Container Registry",
  "/ui/s3": "Simple Storage Service",
  "/ui/logs": "CloudWatch Logs",
  "/ui/organizations": "AWS Organizations",
  "/ui/iam": "Identity and Access Management",
  "/ui/ec2": "EC2",
  "/ui/autoscaling": "EC2 Auto Scaling",
  "/ui/batch": "AWS Batch",
  "/ui/efs": "Elastic File System",
  "/ui/rds": "RDS",
  "/ui/dynamodb": "DynamoDB",
  "/ui/elasticache": "ElastiCache",
  "/ui/vpc": "VPC",
  "/ui/cloudfront": "CloudFront",
  "/ui/route53": "Route 53",
  "/ui/apigateway": "API Gateway",
  "/ui/elb": "Elastic Load Balancing",
  "/ui/cloudmap": "AWS Cloud Map",
  "/ui/codebuild": "CodeBuild",
  "/ui/amplify": "AWS Amplify",
  "/ui/kinesis": "Kinesis Data Streams",
  "/ui/firehose": "Amazon Data Firehose",
  "/ui/glue": "AWS Glue",
  "/ui/sns": "Simple Notification Service",
  "/ui/sqs": "Simple Queue Service",
  "/ui/eventbridge": "Amazon EventBridge",
  "/ui/scheduler": "Amazon EventBridge Scheduler",
  "/ui/stepfunctions": "AWS Step Functions",
  "/ui/cloudwatch": "CloudWatch",
  "/ui/cloudtrail": "AWS CloudTrail",
  "/ui/ssm": "Systems Manager",
  "/ui/secretsmanager": "Secrets Manager",
  "/ui/kms": "Key Management Service",
  "/ui/acm": "AWS Certificate Manager",
  "/ui/private-ca": "AWS Private Certificate Authority",
  "/ui/waf": "AWS WAF",
  "/ui/budgets": "AWS Budgets",
};

const IAM_USER_PREFIX = "/ui/iam/users/";
const ORG_ACCOUNT_PREFIX = "/ui/organizations/accounts/";
const ECS_SERVICE_PREFIX = "/ui/ecs/services/";
const NOT_SUPPORTED_PREFIX = "/ui/not-supported/";

// Resource detail routes nest under their service the way the real console
// breadcrumbs them: <service> > <resource identifier>. Every prefix here has
// a matching `:id`/`:name` route in main.tsx.
const RESOURCE_DETAIL_PREFIXES: { prefix: string; service: string; to: string }[] = [
  { prefix: "/ui/ecs/", service: "Elastic Container Service", to: "/ui/ecs" },
  { prefix: "/ui/lambda/", service: "Lambda", to: "/ui/lambda" },
  { prefix: "/ui/ecr/", service: "Elastic Container Registry", to: "/ui/ecr" },
  { prefix: "/ui/s3/", service: "Simple Storage Service", to: "/ui/s3" },
  { prefix: "/ui/logs/", service: "CloudWatch Logs", to: "/ui/logs" },
  { prefix: "/ui/ec2/", service: "EC2", to: "/ui/ec2" },
  { prefix: "/ui/vpc/", service: "VPC", to: "/ui/vpc" },
  { prefix: "/ui/dynamodb/", service: "DynamoDB", to: "/ui/dynamodb" },
  { prefix: "/ui/efs/", service: "Elastic File System", to: "/ui/efs" },
  { prefix: "/ui/route53/", service: "Route 53", to: "/ui/route53" },
  { prefix: "/ui/stepfunctions/", service: "AWS Step Functions", to: "/ui/stepfunctions" },
];

function crumbTrail(pathname: string): { label: string; to?: string }[] {
  // Detail pages nest under their service the way the real console breadcrumbs
  // them: IAM > Users > <user name>, AWS Organizations > <account id>.
  if (pathname.startsWith(IAM_USER_PREFIX)) {
    return [
      { label: "Simulator", to: "/ui/" },
      { label: "Identity and Access Management", to: "/ui/iam" },
      { label: decodeURIComponent(pathname.slice(IAM_USER_PREFIX.length)) },
    ];
  }
  if (pathname.startsWith(ORG_ACCOUNT_PREFIX)) {
    return [
      { label: "Simulator", to: "/ui/" },
      { label: "AWS Organizations", to: "/ui/organizations" },
      { label: decodeURIComponent(pathname.slice(ORG_ACCOUNT_PREFIX.length)) },
    ];
  }
  if (pathname.startsWith(ECS_SERVICE_PREFIX)) {
    const [cluster = "", service = ""] = pathname.slice(ECS_SERVICE_PREFIX.length).split("/");
    return [
      { label: "Simulator", to: "/ui/" },
      { label: "Elastic Container Service", to: "/ui/ecs" },
      { label: decodeURIComponent(cluster) },
      { label: decodeURIComponent(service) },
    ];
  }
  for (const entry of RESOURCE_DETAIL_PREFIXES) {
    if (pathname.startsWith(entry.prefix)) {
      return [
        { label: "Simulator", to: "/ui/" },
        { label: entry.service, to: entry.to },
        { label: decodeURIComponent(pathname.slice(entry.prefix.length)) },
      ];
    }
  }
  if (pathname.startsWith(NOT_SUPPORTED_PREFIX)) {
    const slug = pathname.slice(NOT_SUPPORTED_PREFIX.length);
    return [
      { label: "Simulator", to: "/ui/" },
      { label: findNavService(slug)?.label ?? slug },
    ];
  }
  return [
    { label: "Simulator", to: "/ui/" },
    { label: CRUMBS[pathname] ?? "Overview" },
  ];
}

function ConsoleFrame({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  // Cloudscape's own `AppLayout` keeps the navigation open by default and
  // owns the toggle control (and its accessible name) that collapses it —
  // this is the only navigation-open state the console keeps.
  const [navigationOpen, setNavigationOpen] = useState(true);
  return (
    <div className="aws">
      <a href="#main-content" className="sl-skip-link">Skip to main content</a>
      <AwsHeader region={REGION} account={<OperatorAccount />} services={NAV_GROUPS} />
      <AppLayout
        navigation={<AwsSideNavigation groups={NAV_GROUPS} activeHref={pathname} />}
        navigationOpen={navigationOpen}
        onNavigationChange={(event) => setNavigationOpen(event.detail.open)}
        breadcrumbs={<AwsBreadcrumbs trail={crumbTrail(pathname)} />}
        toolsHide
        content={
          // AppLayout's own `content` slot already renders inside a real
          // `<main>` landmark — wrapping it in a second one here duplicated
          // the main landmark (axe: landmark-no-duplicate-main). This stays
          // a plain element so `#main-content`/the skip link still has
          // something to focus.
          <div id="main-content" tabIndex={-1}>
            {children}
          </div>
        }
        ariaLabels={{
          navigation: "Service",
          navigationToggle: "Open navigation",
          navigationClose: "Close navigation",
        }}
      />
    </div>
  );
}

export function AwsApp({ children }: { children: ReactNode }) {
  return (
    <ErrorBoundary>
      <BrowserRouter>
        <ConsoleFrame>
          <Routes>{children}</Routes>
        </ConsoleFrame>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
