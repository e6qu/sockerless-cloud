import { useNavigate } from "react-router";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Grid from "@cloudscape-design/components/grid";
import Box from "@cloudscape-design/components/box";
import Link from "@cloudscape-design/components/link";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Button from "@cloudscape-design/components/button";
import { findService, type ConsoleService } from "../console/serviceCatalog.js";
import { useRecentlyVisited } from "../console/recent.js";
import { useOpenServicesMenu } from "../console/AwsApp.js";

// The real console's Build a solution widget: tasks that open the console flow
// that does them.
const SOLUTIONS = [
  { text: "Create a bucket", with: "With S3", href: "/ui/s3/create" },
  { text: "Create a function", with: "With Lambda", href: "/ui/lambda" },
  { text: "Create a table", with: "With DynamoDB", href: "/ui/dynamodb" },
  { text: "Create a queue", with: "With SQS", href: "/ui/sqs" },
  { text: "Create a role", with: "With IAM", href: "/ui/iam/roles/create" },
  { text: "Create a key pair", with: "With EC2", href: "/ui/ec2/key-pairs/create" },
];

const WELCOME = [
  {
    text: "Getting started with AWS",
    description: "Learn the fundamentals and find valuable information to get the most out of AWS.",
    href: "https://aws.amazon.com/getting-started/",
  },
  {
    text: "Training and certification",
    description: "Learn from AWS experts and advance your skills and knowledge.",
    href: "https://aws.amazon.com/training/",
  },
  {
    text: "What's new with AWS?",
    description: "Discover new AWS services, features, and Regions.",
    href: "https://aws.amazon.com/new/",
  },
];

function ConsoleLink({ href, children }: { href: string; children: string }) {
  const navigate = useNavigate();
  return (
    <Link
      href={href}
      onFollow={(event) => {
        event.preventDefault();
        navigate(href);
      }}
    >
      {children}
    </Link>
  );
}

function RecentlyVisited() {
  const navigate = useNavigate();
  const openServicesMenu = useOpenServicesMenu();
  const services = useRecentlyVisited()
    .map(findService)
    .filter((service): service is ConsoleService => Boolean(service));
  return (
    <Container
      fitHeight
      header={<Header variant="h2">Recently visited</Header>}
      footer={
        <Box textAlign="center">
          <Link
            href="#services"
            onFollow={(event) => {
              event.preventDefault();
              openServicesMenu();
            }}
          >
            View all services
          </Link>
        </Box>
      }
    >
      {services.length === 0 ? (
        <Box textAlign="center" color="inherit" padding={{ vertical: "m" }}>
          <Box variant="strong" color="inherit">
            No recently visited services
          </Box>
          <Box variant="p" color="inherit" padding={{ bottom: "s" }}>
            Explore AWS services from the Services menu.
          </Box>
          <Button onClick={() => navigate("/ui/s3")}>Go to S3</Button>
        </Box>
      ) : (
        <ul className="aws-home-list" aria-label="Recently visited services">
          {services.map((service) => (
            <li key={service.id}>
              <ConsoleLink href={service.home}>{service.name}</ConsoleLink>
            </li>
          ))}
        </ul>
      )}
    </Container>
  );
}

function BuildASolution() {
  return (
    <Container fitHeight header={<Header variant="h2">Build a solution</Header>}>
      <ul className="aws-home-solutions" aria-label="Build a solution">
        {SOLUTIONS.map((solution) => (
          <li key={solution.href}>
            <ConsoleLink href={solution.href}>{solution.text}</ConsoleLink>
            <Box fontSize="body-s" color="text-body-secondary">
              {solution.with}
            </Box>
          </li>
        ))}
      </ul>
    </Container>
  );
}

function WelcomeToAws() {
  return (
    <Container fitHeight header={<Header variant="h2">Welcome to AWS</Header>}>
      <SpaceBetween size="l">
        {WELCOME.map((entry) => (
          <div key={entry.href}>
            <Link href={entry.href} external fontSize="heading-xs">
              {entry.text}
            </Link>
            <Box color="text-body-secondary">{entry.description}</Box>
          </div>
        ))}
      </SpaceBetween>
    </Container>
  );
}

export function ConsoleHomePage() {
  return (
    <ContentLayout header={<Header variant="h1">Console Home</Header>}>
      <Grid
        gridDefinition={[
          { colspan: { default: 12, m: 6 } },
          { colspan: { default: 12, m: 6 } },
          { colspan: { default: 12, m: 6 } },
        ]}
      >
        <RecentlyVisited />
        <WelcomeToAws />
        <BuildASolution />
      </Grid>
    </ContentLayout>
  );
}
