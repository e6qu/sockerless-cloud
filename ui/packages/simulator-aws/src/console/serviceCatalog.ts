/**
 * The AWS service catalog the console's shell renders: the Services menu, the
 * unified search, Console Home's recently visited list, every service's own
 * side navigation, and the breadcrumb trail all read from this one table.
 *
 * The real AWS Management Console lists every service AWS offers under the
 * category AWS files it in, whether or not an account uses it. This console
 * does the same and is honest about its own reach: a service it has pages for
 * links to them; every other service still appears, under its real category,
 * and links to a page that says this console has no page for it. That is a
 * statement about the console, not about the cloud it reads, so it holds
 * whichever AWS endpoint the console is pointed at.
 */

export interface NavLink {
  text: string;
  href: string;
  /** The page title of the link's `<href>/create` flow, for its breadcrumb. */
  create?: string;
}

export interface NavSection {
  /** The section heading the real service console's side navigation shows;
   * absent for the links that sit above the first heading. */
  text?: string;
  items: NavLink[];
}

export interface ConsoleService {
  id: string;
  /** The name the AWS Services menu shows. */
  name: string;
  /** The one-line description the AWS Services menu shows under the name. */
  description: string;
  category: string;
  /** The service's landing route in this console. */
  home: string;
  /** The service's documentation landing page. */
  docs: string;
  /** Route prefixes this service's pages live under, beyond `home`. */
  owns?: string[];
  /** The service console's own side navigation. */
  nav?: NavSection[];
  /** False for a service this console has no pages for. */
  supported?: boolean;
}

const NO_PAGE = "/ui/not-supported/";

function unsupported(id: string, name: string, description: string, category: string, docs: string): ConsoleService {
  return { id, name, description, category, home: NO_PAGE + id, docs, supported: false };
}

export const CATEGORIES = [
  "Analytics",
  "Application Integration",
  "Cloud Financial Management",
  "Compute",
  "Containers",
  "Database",
  "Developer Tools",
  "Front-end Web & Mobile",
  "Management & Governance",
  "Networking & Content Delivery",
  "Security, Identity, & Compliance",
  "Storage",
] as const;

export const SERVICES: ConsoleService[] = [
  {
    id: "ec2",
    name: "EC2",
    description: "Virtual Servers in the Cloud",
    category: "Compute",
    home: "/ui/ec2/home",
    docs: "https://docs.aws.amazon.com/ec2/",
    owns: ["/ui/ec2", "/ui/autoscaling", "/ui/elb"],
    nav: [
      { items: [{ text: "Dashboard", href: "/ui/ec2/home" }] },
      { text: "Instances", items: [{ text: "Instances", href: "/ui/ec2" }] },
      { text: "Elastic Block Store", items: [{ text: "Volumes", href: "/ui/ec2/volumes" }] },
      {
        text: "Network & Security",
        items: [
          { text: "Security Groups", href: "/ui/ec2/security-groups" },
          { text: "Key Pairs", href: "/ui/ec2/key-pairs", create: "Create key pair" },
        ],
      },
      {
        text: "Load Balancing",
        items: [
          { text: "Load Balancers", href: "/ui/elb" },
          { text: "Target Groups", href: "/ui/elb/target-groups" },
        ],
      },
      { text: "Auto Scaling", items: [{ text: "Auto Scaling Groups", href: "/ui/autoscaling" }] },
    ],
  },
  {
    id: "lambda",
    name: "Lambda",
    description: "Run code without thinking about servers",
    category: "Compute",
    home: "/ui/lambda",
    docs: "https://docs.aws.amazon.com/lambda/",
    nav: [{ items: [{ text: "Functions", href: "/ui/lambda" }] }],
  },
  {
    id: "batch",
    name: "Batch",
    description: "Fully managed batch processing at any scale",
    category: "Compute",
    home: "/ui/batch",
    docs: "https://docs.aws.amazon.com/batch/",
    nav: [
      {
        items: [
          { text: "Jobs", href: "/ui/batch" },
          { text: "Job queues", href: "/ui/batch/job-queues" },
          { text: "Job definitions", href: "/ui/batch/job-definitions" },
          { text: "Compute environments", href: "/ui/batch/compute-environments" },
        ],
      },
    ],
  },
  unsupported("elastic-beanstalk", "Elastic Beanstalk", "Run and Manage Web Apps", "Compute", "https://docs.aws.amazon.com/elastic-beanstalk/"),
  unsupported("lightsail", "Lightsail", "Launch and Manage Virtual Private Servers", "Compute", "https://docs.aws.amazon.com/lightsail/"),
  {
    id: "ecs",
    name: "Elastic Container Service",
    description: "Highly secure, reliable, and scalable way to run containers",
    category: "Containers",
    home: "/ui/ecs",
    docs: "https://docs.aws.amazon.com/ecs/",
    nav: [{ items: [{ text: "Tasks", href: "/ui/ecs" }, { text: "Services", href: "/ui/ecs/services" }] }],
  },
  {
    id: "ecr",
    name: "Elastic Container Registry",
    description: "Fully-managed Docker container registry",
    category: "Containers",
    home: "/ui/ecr",
    docs: "https://docs.aws.amazon.com/ecr/",
    nav: [{ text: "Private registry", items: [{ text: "Repositories", href: "/ui/ecr" }] }],
  },
  unsupported("eks", "Elastic Kubernetes Service", "The most trusted way to start, run, and scale Kubernetes", "Containers", "https://docs.aws.amazon.com/eks/"),
  {
    id: "s3",
    name: "S3",
    description: "Scalable Storage in the Cloud",
    category: "Storage",
    home: "/ui/s3",
    docs: "https://docs.aws.amazon.com/s3/",
    nav: [{ items: [{ text: "General purpose buckets", href: "/ui/s3", create: "Create bucket" }] }],
  },
  {
    id: "efs",
    name: "EFS",
    description: "Managed File Storage for EC2",
    category: "Storage",
    home: "/ui/efs",
    docs: "https://docs.aws.amazon.com/efs/",
    nav: [{ text: "Elastic File System", items: [{ text: "File systems", href: "/ui/efs" }] }],
  },
  unsupported("s3-glacier", "S3 Glacier", "Archive Storage in the Cloud", "Storage", "https://docs.aws.amazon.com/amazonglacier/"),
  unsupported("backup", "AWS Backup", "Centrally manage and automate backups across AWS services", "Storage", "https://docs.aws.amazon.com/aws-backup/"),
  {
    id: "rds",
    name: "Aurora and RDS",
    description: "Managed Relational Database Service",
    category: "Database",
    home: "/ui/rds",
    docs: "https://docs.aws.amazon.com/rds/",
    nav: [{ items: [{ text: "Databases", href: "/ui/rds" }] }],
  },
  {
    id: "dynamodb",
    name: "DynamoDB",
    description: "Managed NoSQL Database",
    category: "Database",
    home: "/ui/dynamodb",
    docs: "https://docs.aws.amazon.com/dynamodb/",
    nav: [{ items: [{ text: "Tables", href: "/ui/dynamodb" }] }],
  },
  {
    id: "elasticache",
    name: "ElastiCache",
    description: "In-Memory Cache",
    category: "Database",
    home: "/ui/elasticache",
    docs: "https://docs.aws.amazon.com/elasticache/",
    nav: [{ text: "Resources", items: [{ text: "Clusters", href: "/ui/elasticache" }] }],
  },
  unsupported("redshift", "Amazon Redshift", "Fast, Simple, Cost-Effective Data Warehousing", "Database", "https://docs.aws.amazon.com/redshift/"),
  {
    id: "vpc",
    name: "VPC",
    description: "Isolated Cloud Resources",
    category: "Networking & Content Delivery",
    home: "/ui/vpc",
    docs: "https://docs.aws.amazon.com/vpc/",
    nav: [{ text: "Virtual private cloud", items: [{ text: "Your VPCs", href: "/ui/vpc" }] }],
  },
  {
    id: "cloudfront",
    name: "CloudFront",
    description: "Global Content Delivery Network",
    category: "Networking & Content Delivery",
    home: "/ui/cloudfront",
    docs: "https://docs.aws.amazon.com/cloudfront/",
    nav: [{ items: [{ text: "Distributions", href: "/ui/cloudfront" }] }],
  },
  {
    id: "route53",
    name: "Route 53",
    description: "Scalable DNS and Domain Name Registration",
    category: "Networking & Content Delivery",
    home: "/ui/route53",
    docs: "https://docs.aws.amazon.com/route53/",
    nav: [{ items: [{ text: "Hosted zones", href: "/ui/route53" }] }],
  },
  {
    id: "apigateway",
    name: "API Gateway",
    description: "Build, Deploy and Manage APIs",
    category: "Networking & Content Delivery",
    home: "/ui/apigateway",
    docs: "https://docs.aws.amazon.com/apigateway/",
    nav: [{ items: [{ text: "APIs", href: "/ui/apigateway" }] }],
  },
  {
    id: "cloudmap",
    name: "Cloud Map",
    description: "Build a dynamic map of your cloud",
    category: "Networking & Content Delivery",
    home: "/ui/cloudmap",
    docs: "https://docs.aws.amazon.com/cloud-map/",
    nav: [{ items: [{ text: "Namespaces", href: "/ui/cloudmap" }, { text: "Services", href: "/ui/cloudmap/services" }] }],
  },
  unsupported("direct-connect", "Direct Connect", "Dedicated Network Connection to AWS", "Networking & Content Delivery", "https://docs.aws.amazon.com/directconnect/"),
  {
    id: "codebuild",
    name: "CodeBuild",
    description: "Build and Test Code",
    category: "Developer Tools",
    home: "/ui/codebuild",
    docs: "https://docs.aws.amazon.com/codebuild/",
    nav: [{ text: "Build", items: [{ text: "Build projects", href: "/ui/codebuild" }, { text: "Build history", href: "/ui/codebuild/history" }] }],
  },
  unsupported("codepipeline", "CodePipeline", "Release Software using Continuous Delivery", "Developer Tools", "https://docs.aws.amazon.com/codepipeline/"),
  {
    id: "amplify",
    name: "AWS Amplify",
    description: "Build, deploy, host, and manage scalable web and mobile apps",
    category: "Front-end Web & Mobile",
    home: "/ui/amplify",
    docs: "https://docs.aws.amazon.com/amplify/",
    nav: [{ items: [{ text: "All apps", href: "/ui/amplify" }] }],
  },
  {
    id: "kinesis",
    name: "Kinesis",
    description: "Work with Real-Time Streaming Data",
    category: "Analytics",
    home: "/ui/kinesis",
    docs: "https://docs.aws.amazon.com/kinesis/",
    nav: [{ items: [{ text: "Data streams", href: "/ui/kinesis" }] }],
  },
  {
    id: "firehose",
    name: "Amazon Data Firehose",
    description: "Reliably load real-time streams into data lakes, warehouses, and analytics services",
    category: "Analytics",
    home: "/ui/firehose",
    docs: "https://docs.aws.amazon.com/firehose/",
    nav: [{ items: [{ text: "Firehose streams", href: "/ui/firehose" }] }],
  },
  {
    id: "glue",
    name: "AWS Glue",
    description: "Serverless data integration service",
    category: "Analytics",
    home: "/ui/glue",
    docs: "https://docs.aws.amazon.com/glue/",
    nav: [
      { text: "Data Catalog", items: [{ text: "Databases", href: "/ui/glue" }] },
      { text: "ETL", items: [{ text: "ETL jobs", href: "/ui/glue/jobs" }] },
    ],
  },
  unsupported("athena", "Athena", "Query Data in S3 using SQL", "Analytics", "https://docs.aws.amazon.com/athena/"),
  unsupported("emr", "EMR", "Managed Hadoop Framework", "Analytics", "https://docs.aws.amazon.com/emr/"),
  {
    id: "sns",
    name: "Simple Notification Service",
    description: "SNS managed message topics for Pub/Sub",
    category: "Application Integration",
    home: "/ui/sns",
    docs: "https://docs.aws.amazon.com/sns/",
    nav: [{ items: [{ text: "Topics", href: "/ui/sns" }, { text: "Subscriptions", href: "/ui/sns/subscriptions" }] }],
  },
  {
    id: "sqs",
    name: "Simple Queue Service",
    description: "Managed Message Queues",
    category: "Application Integration",
    home: "/ui/sqs",
    docs: "https://docs.aws.amazon.com/sqs/",
    nav: [{ items: [{ text: "Queues", href: "/ui/sqs" }] }],
  },
  {
    id: "eventbridge",
    name: "Amazon EventBridge",
    description: "Serverless service for building event-driven applications",
    category: "Application Integration",
    home: "/ui/eventbridge",
    docs: "https://docs.aws.amazon.com/eventbridge/",
    owns: ["/ui/scheduler"],
    nav: [
      {
        text: "Buses",
        items: [
          { text: "Event buses", href: "/ui/eventbridge/event-buses" },
          { text: "Rules", href: "/ui/eventbridge" },
        ],
      },
      {
        text: "Scheduler",
        items: [
          { text: "Schedules", href: "/ui/scheduler" },
          { text: "Schedule groups", href: "/ui/scheduler/groups" },
        ],
      },
    ],
  },
  {
    id: "stepfunctions",
    name: "Step Functions",
    description: "Coordination for Distributed Applications",
    category: "Application Integration",
    home: "/ui/stepfunctions",
    docs: "https://docs.aws.amazon.com/step-functions/",
    nav: [{ items: [{ text: "State machines", href: "/ui/stepfunctions" }] }],
  },
  unsupported("mq", "Amazon MQ", "Managed message broker service", "Application Integration", "https://docs.aws.amazon.com/amazon-mq/"),
  {
    id: "budgets",
    name: "Budgets",
    description: "Set custom cost and usage budgets",
    category: "Cloud Financial Management",
    home: "/ui/budgets",
    docs: "https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html",
    nav: [{ text: "Budgets and planning", items: [{ text: "Budgets", href: "/ui/budgets" }] }],
  },
  unsupported("cost-explorer", "Cost Explorer", "Visualize, understand, and manage your AWS costs and usage", "Cloud Financial Management", "https://docs.aws.amazon.com/cost-management/latest/userguide/ce-what-is.html"),
  {
    id: "cloudwatch",
    name: "CloudWatch",
    description: "Monitor Resources and Applications",
    category: "Management & Governance",
    home: "/ui/cloudwatch",
    docs: "https://docs.aws.amazon.com/cloudwatch/",
    owns: ["/ui/logs"],
    nav: [
      { text: "Alarms", items: [{ text: "All alarms", href: "/ui/cloudwatch" }] },
      { text: "Logs", items: [{ text: "Log groups", href: "/ui/logs" }] },
      { text: "Dashboards", items: [{ text: "Dashboards", href: "/ui/cloudwatch/dashboards" }] },
    ],
  },
  {
    id: "cloudtrail",
    name: "CloudTrail",
    description: "Track User Activity and API Usage",
    category: "Management & Governance",
    home: "/ui/cloudtrail",
    docs: "https://docs.aws.amazon.com/cloudtrail/",
    nav: [{ items: [{ text: "Event history", href: "/ui/cloudtrail" }, { text: "Trails", href: "/ui/cloudtrail/trails" }] }],
  },
  {
    id: "organizations",
    name: "AWS Organizations",
    description: "Central governance and management across AWS accounts",
    category: "Management & Governance",
    home: "/ui/organizations",
    docs: "https://docs.aws.amazon.com/organizations/",
    nav: [{ items: [{ text: "AWS accounts", href: "/ui/organizations" }] }],
  },
  {
    id: "ssm",
    name: "Systems Manager",
    description: "Access, manage, and configure your AWS resources",
    category: "Management & Governance",
    home: "/ui/ssm",
    docs: "https://docs.aws.amazon.com/systems-manager/",
    nav: [
      {
        text: "Application Tools",
        items: [{ text: "Parameter Store", href: "/ui/ssm" }],
      },
      { text: "Change Management Tools", items: [{ text: "Documents", href: "/ui/ssm/documents" }] },
    ],
  },
  unsupported("cloudformation", "CloudFormation", "Create and Manage Resources with Templates", "Management & Governance", "https://docs.aws.amazon.com/cloudformation/"),
  unsupported("config", "Config", "Track Resource Inventory and Changes", "Management & Governance", "https://docs.aws.amazon.com/config/"),
  {
    id: "iam",
    name: "IAM",
    description: "Manage access to AWS resources",
    category: "Security, Identity, & Compliance",
    home: "/ui/iam",
    docs: "https://docs.aws.amazon.com/iam/",
    nav: [
      {
        text: "Access management",
        items: [
          { text: "Users", href: "/ui/iam" },
          { text: "Roles", href: "/ui/iam/roles", create: "Create role" },
          { text: "Policies", href: "/ui/iam/policies", create: "Create policy" },
          { text: "Identity providers", href: "/ui/iam/identity-providers" },
        ],
      },
    ],
  },
  {
    id: "secretsmanager",
    name: "Secrets Manager",
    description: "Easily rotate, manage, and retrieve secrets throughout their lifecycle",
    category: "Security, Identity, & Compliance",
    home: "/ui/secretsmanager",
    docs: "https://docs.aws.amazon.com/secretsmanager/",
    nav: [{ items: [{ text: "Secrets", href: "/ui/secretsmanager" }] }],
  },
  {
    id: "kms",
    name: "Key Management Service",
    description: "Securely Generate and Manage AWS Encryption Keys",
    category: "Security, Identity, & Compliance",
    home: "/ui/kms",
    docs: "https://docs.aws.amazon.com/kms/",
    nav: [{ items: [{ text: "Customer managed keys", href: "/ui/kms" }] }],
  },
  {
    id: "acm",
    name: "Certificate Manager",
    description: "Provision, Manage, and Deploy SSL/TLS Certificates",
    category: "Security, Identity, & Compliance",
    home: "/ui/acm",
    docs: "https://docs.aws.amazon.com/acm/",
    nav: [{ items: [{ text: "List certificates", href: "/ui/acm" }, { text: "ACME endpoints", href: "/ui/acm/acme-endpoints" }] }],
  },
  {
    id: "private-ca",
    name: "AWS Private Certificate Authority",
    description: "Create private certificates to identify resources and protect data",
    category: "Security, Identity, & Compliance",
    home: "/ui/private-ca",
    docs: "https://docs.aws.amazon.com/privateca/",
    nav: [{ items: [{ text: "Private certificate authorities", href: "/ui/private-ca" }] }],
  },
  {
    id: "waf",
    name: "WAF & Shield",
    description: "Protect your web applications from common web exploits",
    category: "Security, Identity, & Compliance",
    home: "/ui/waf",
    docs: "https://docs.aws.amazon.com/waf/",
    nav: [{ text: "AWS WAF", items: [{ text: "Web ACLs", href: "/ui/waf" }, { text: "IP sets", href: "/ui/waf/ip-sets" }] }],
  },
  unsupported("cognito", "Cognito", "Consumer Identity Management and AWS Credentials for Mobile Apps", "Security, Identity, & Compliance", "https://docs.aws.amazon.com/cognito/"),
  unsupported("guardduty", "GuardDuty", "Intelligent Threat Detection to Protect Your AWS Accounts and Workloads", "Security, Identity, & Compliance", "https://docs.aws.amazon.com/guardduty/"),
];

export function findService(id: string): ConsoleService | undefined {
  return SERVICES.find((service) => service.id === id);
}

function underPrefix(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

/** The service whose pages a route belongs to, by the longest route prefix it
 * owns; undefined for Console Home. */
export function serviceForPath(pathname: string): ConsoleService | undefined {
  if (pathname.startsWith(NO_PAGE)) return findService(pathname.slice(NO_PAGE.length));
  let best: { service: ConsoleService; length: number } | undefined;
  for (const service of SERVICES) {
    if (service.supported === false) continue;
    const prefixes = [service.home, ...(service.owns ?? []), ...(service.nav ?? []).flatMap((section) => section.items.map((item) => item.href))];
    for (const prefix of prefixes) {
      if (underPrefix(pathname, prefix) && (!best || prefix.length > best.length)) {
        best = { service, length: prefix.length };
      }
    }
  }
  return best?.service;
}

/** The side-navigation link a route sits under: an exact match first, then the
 * longest link whose route is a parent of it. */
export function navLinkForPath(service: ConsoleService, pathname: string): { link: NavLink; exact: boolean } | undefined {
  const links = (service.nav ?? []).flatMap((section) => section.items);
  const exact = links.find((link) => link.href === pathname);
  if (exact) return { link: exact, exact: true };
  const parents = links.filter((link) => pathname.startsWith(link.href + "/")).sort((a, b) => b.href.length - a.href.length);
  return parents[0] ? { link: parents[0], exact: false } : undefined;
}

export interface SearchEntry {
  /** "service" for a service, "feature" for one of its side-navigation pages. */
  kind: "service" | "feature";
  label: string;
  description: string;
  href: string;
  service: ConsoleService;
}

/** Every service and every service feature the unified search can find. */
export const SEARCH_ENTRIES: SearchEntry[] = SERVICES.flatMap((service) => [
  { kind: "service" as const, label: service.name, description: service.description, href: service.home, service },
  ...(service.nav ?? []).flatMap((section) =>
    section.items.map((item) => ({
      kind: "feature" as const,
      label: item.text,
      description: service.name,
      href: item.href,
      service,
    })),
  ),
]);

/**
 * Narrows the search index to entries whose name or service matches the
 * query, case-insensitively and by substring. Services rank ahead of features,
 * and a name that starts with the query ranks ahead of one that contains it.
 */
export function searchConsole(query: string, entries: SearchEntry[] = SEARCH_ENTRIES): SearchEntry[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [];
  const rank = (entry: SearchEntry) => {
    const label = entry.label.toLowerCase();
    const position = label.indexOf(needle);
    const base = entry.kind === "service" ? 0 : 10;
    if (position === 0) return base;
    if (position > 0) return base + 1;
    return base + 2;
  };
  return entries
    .filter(
      (entry) =>
        entry.label.toLowerCase().includes(needle) ||
        entry.service.name.toLowerCase().includes(needle) ||
        (entry.kind === "service" && entry.description.toLowerCase().includes(needle)),
    )
    .sort((a, b) => rank(a) - rank(b) || a.label.localeCompare(b.label));
}

/** The services of one category, or of the whole catalog, whose name matches
 * the Services menu's filter. */
export function filterServices(query: string, category?: string): ConsoleService[] {
  const needle = query.trim().toLowerCase();
  return SERVICES.filter(
    (service) =>
      (!category || service.category === category) &&
      (!needle || service.name.toLowerCase().includes(needle) || service.description.toLowerCase().includes(needle)),
  ).sort((a, b) => a.name.localeCompare(b.name));
}
