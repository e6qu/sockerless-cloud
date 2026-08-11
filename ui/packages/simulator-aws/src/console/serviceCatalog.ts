/**
 * The AWS service catalog the console's side navigation renders.
 *
 * The real AWS Management Console groups its "All services" menu the way AWS
 * itself categorizes its services — Compute, Containers, Storage, Database,
 * Networking & Content Delivery, and so on — and lists every service AWS
 * offers, not only the ones a given account uses. This simulator follows the
 * same shape and is honest about the gap between the two: a service
 * sockerless implements links to its real page; every other service AWS
 * offers is still listed, under its real category, but links to a page that
 * says plainly that sockerless has not implemented it. Hiding the
 * unimplemented services would make the simulator's coverage look broader
 * than it is; leaving AWS's other categories out entirely would make the
 * navigation read as sockerless's own invention rather than AWS's console.
 *
 * Honesty runs in both directions. An entry marked `supported: false` is a
 * claim that the AWS simulator implements nothing an operator could use, and
 * it is only ever correct when the simulator registers no usable read surface
 * for that service. Every entry below with a real route is backed by the
 * simulator's own registered operations — Amazon EC2's `DescribeInstances`,
 * Amazon DynamoDB's `ListTables`, AWS Systems Manager's `DescribeParameters`,
 * and so on — reached through the same AWS APIs a real console reads.
 */

export interface NavService {
  /** The service's real, fully-qualified AWS name — the link's visible and
   * accessible text. */
  label: string;
  to: string;
  /** Services sockerless has built a page for. Defaults to true; every entry
   * that sockerless has not implemented links to the honest "not supported"
   * page instead of a real console page, and its link renders the
   * "Not supported" pill. */
  supported?: boolean;
}

export interface NavGroup {
  /** The category AWS's own "All services" menu groups this service under. */
  label: string;
  items: NavService[];
}

const NOT_SUPPORTED = "/ui/not-supported/";

export const NAV_GROUPS: NavGroup[] = [
  { label: "Dashboard", items: [{ label: "Overview", to: "/ui/" }] },
  {
    label: "Compute",
    items: [
      { label: "EC2", to: "/ui/ec2" },
      { label: "EC2 Auto Scaling", to: "/ui/autoscaling" },
      { label: "Lambda", to: "/ui/lambda" },
      { label: "AWS Batch", to: "/ui/batch" },
      { label: "Elastic Beanstalk", to: NOT_SUPPORTED + "elastic-beanstalk", supported: false },
      { label: "Lightsail", to: NOT_SUPPORTED + "lightsail", supported: false },
    ],
  },
  {
    label: "Containers",
    items: [
      { label: "Elastic Container Service", to: "/ui/ecs" },
      { label: "Elastic Container Registry", to: "/ui/ecr" },
      { label: "Elastic Kubernetes Service", to: NOT_SUPPORTED + "eks", supported: false },
    ],
  },
  {
    label: "Storage",
    items: [
      { label: "Simple Storage Service", to: "/ui/s3" },
      { label: "Elastic File System", to: "/ui/efs" },
      { label: "S3 Glacier", to: NOT_SUPPORTED + "s3-glacier", supported: false },
      { label: "AWS Backup", to: NOT_SUPPORTED + "backup", supported: false },
    ],
  },
  {
    label: "Database",
    items: [
      { label: "RDS", to: "/ui/rds" },
      { label: "DynamoDB", to: "/ui/dynamodb" },
      { label: "ElastiCache", to: "/ui/elasticache" },
      { label: "Amazon Redshift", to: NOT_SUPPORTED + "redshift", supported: false },
    ],
  },
  {
    label: "Networking & content delivery",
    items: [
      { label: "VPC", to: "/ui/vpc" },
      { label: "CloudFront", to: "/ui/cloudfront" },
      { label: "Route 53", to: "/ui/route53" },
      { label: "API Gateway", to: "/ui/apigateway" },
      { label: "Elastic Load Balancing", to: "/ui/elb" },
      { label: "AWS Cloud Map", to: "/ui/cloudmap" },
      { label: "AWS Direct Connect", to: NOT_SUPPORTED + "direct-connect", supported: false },
    ],
  },
  {
    label: "Developer tools",
    items: [
      { label: "CodeBuild", to: "/ui/codebuild" },
      { label: "CodePipeline", to: NOT_SUPPORTED + "codepipeline", supported: false },
    ],
  },
  {
    label: "Front-end web & mobile",
    items: [{ label: "AWS Amplify", to: "/ui/amplify" }],
  },
  {
    label: "Analytics",
    items: [
      { label: "Kinesis Data Streams", to: "/ui/kinesis" },
      { label: "Amazon Data Firehose", to: "/ui/firehose" },
      { label: "AWS Glue", to: "/ui/glue" },
      { label: "Amazon Athena", to: NOT_SUPPORTED + "athena", supported: false },
      { label: "Amazon EMR", to: NOT_SUPPORTED + "emr", supported: false },
    ],
  },
  {
    label: "Application integration",
    items: [
      { label: "Simple Notification Service", to: "/ui/sns" },
      { label: "Simple Queue Service", to: "/ui/sqs" },
      { label: "Amazon EventBridge", to: "/ui/eventbridge" },
      { label: "Amazon EventBridge Scheduler", to: "/ui/scheduler" },
      { label: "AWS Step Functions", to: "/ui/stepfunctions" },
      { label: "Amazon MQ", to: NOT_SUPPORTED + "mq", supported: false },
    ],
  },
  {
    label: "Cloud financial management",
    items: [
      { label: "AWS Budgets", to: "/ui/budgets" },
      { label: "AWS Cost Explorer", to: NOT_SUPPORTED + "cost-explorer", supported: false },
    ],
  },
  {
    label: "Management & Governance",
    items: [
      { label: "CloudWatch", to: "/ui/cloudwatch" },
      { label: "CloudWatch Logs", to: "/ui/logs" },
      { label: "AWS CloudTrail", to: "/ui/cloudtrail" },
      { label: "AWS Organizations", to: "/ui/organizations" },
      { label: "Systems Manager", to: "/ui/ssm" },
      { label: "CloudFormation", to: NOT_SUPPORTED + "cloudformation", supported: false },
      { label: "AWS Config", to: NOT_SUPPORTED + "config", supported: false },
    ],
  },
  {
    label: "Security, identity, and compliance",
    items: [
      { label: "Identity and Access Management", to: "/ui/iam" },
      { label: "Secrets Manager", to: "/ui/secretsmanager" },
      { label: "Key Management Service", to: "/ui/kms" },
      { label: "AWS Certificate Manager", to: "/ui/acm" },
      { label: "AWS Private Certificate Authority", to: "/ui/private-ca" },
      { label: "AWS WAF", to: "/ui/waf" },
      { label: "Amazon Cognito", to: NOT_SUPPORTED + "cognito", supported: false },
      { label: "GuardDuty", to: NOT_SUPPORTED + "guardduty", supported: false },
    ],
  },
];

/** Looks up a not-supported service's real name from the slug in its route
 * (`/ui/not-supported/:service`), so the honest page and the breadcrumb trail
 * state the service AWS itself calls it, not the URL slug. */
export function findNavService(slug: string): NavService | undefined {
  const to = NOT_SUPPORTED + slug;
  for (const group of NAV_GROUPS) {
    const found = group.items.find((item) => item.to === to);
    if (found) return found;
  }
  return undefined;
}

/**
 * Narrows the catalog to services whose name matches `query`, the way the
 * real console's "Services" mega-menu search field narrows its grouped
 * columns as an operator types. Matching is case-insensitive and by
 * substring, on the service's real name only — never on the category label,
 * since a match on "Containers" itself would keep every unrelated service in
 * that category visible. A category with no matching service is dropped
 * entirely rather than rendered empty. An empty (or all-whitespace) query
 * returns every group and every item, unfiltered.
 */
export function filterNavGroups(groups: NavGroup[], query: string): NavGroup[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return groups;
  return groups
    .map((group) => ({
      label: group.label,
      items: group.items.filter((item) => item.label.toLowerCase().includes(needle)),
    }))
    .filter((group) => group.items.length > 0);
}
