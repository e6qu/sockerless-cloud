import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Navigate, Route } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AwsApp } from "./console/index.js";
import { OverviewPage } from "./pages/OverviewPage.js";
import { ECSTasksPage } from "./pages/ECSTasksPage.js";
import { ECSTaskDetailPage } from "./pages/ECSTaskDetailPage.js";
import { ECSServiceDetailPage } from "./pages/ECSServiceDetailPage.js";
import { LambdaFunctionsPage } from "./pages/LambdaFunctionsPage.js";
import { LambdaFunctionDetailPage } from "./pages/LambdaFunctionDetailPage.js";
import { ECRReposPage } from "./pages/ECRReposPage.js";
import { ECRRepositoryDetailPage } from "./pages/ECRRepositoryDetailPage.js";
import { S3BucketsPage } from "./pages/S3BucketsPage.js";
import { S3BucketDetailPage } from "./pages/S3BucketDetailPage.js";
import { LogGroupsPage } from "./pages/LogGroupsPage.js";
import { LogGroupDetailPage } from "./pages/LogGroupDetailPage.js";
import { IAMUsersPage } from "./pages/IAMUsersPage.js";
import { IAMUserSecurityCredentialsPage } from "./pages/IAMUserSecurityCredentialsPage.js";
import { OrganizationsPage } from "./pages/OrganizationsPage.js";
import { OrgAccountDetailPage } from "./pages/OrgAccountDetailPage.js";
import { EC2InstancesPage } from "./pages/EC2InstancesPage.js";
import { EC2InstanceDetailPage } from "./pages/EC2InstanceDetailPage.js";
import { AutoScalingGroupsPage } from "./pages/AutoScalingGroupsPage.js";
import { BatchPage } from "./pages/BatchPage.js";
import { EFSFileSystemsPage } from "./pages/EFSFileSystemsPage.js";
import { EFSFileSystemDetailPage } from "./pages/EFSFileSystemDetailPage.js";
import { RDSPage } from "./pages/RDSPage.js";
import { DynamoDBTablesPage } from "./pages/DynamoDBTablesPage.js";
import { DynamoDBTableDetailPage } from "./pages/DynamoDBTableDetailPage.js";
import { ElastiCachePage } from "./pages/ElastiCachePage.js";
import { VPCPage } from "./pages/VPCPage.js";
import { VPCDetailPage } from "./pages/VPCDetailPage.js";
import { CloudFrontPage } from "./pages/CloudFrontPage.js";
import { Route53Page } from "./pages/Route53Page.js";
import { Route53HostedZoneDetailPage } from "./pages/Route53HostedZoneDetailPage.js";
import { APIGatewayPage } from "./pages/APIGatewayPage.js";
import { LoadBalancersPage } from "./pages/LoadBalancersPage.js";
import { CloudMapPage } from "./pages/CloudMapPage.js";
import { CodeBuildPage } from "./pages/CodeBuildPage.js";
import { AmplifyPage } from "./pages/AmplifyPage.js";
import { KinesisPage } from "./pages/KinesisPage.js";
import { FirehosePage } from "./pages/FirehosePage.js";
import { GluePage } from "./pages/GluePage.js";
import { SNSPage } from "./pages/SNSPage.js";
import { SQSPage } from "./pages/SQSPage.js";
import { EventBridgePage } from "./pages/EventBridgePage.js";
import { SchedulerPage } from "./pages/SchedulerPage.js";
import { StepFunctionsPage } from "./pages/StepFunctionsPage.js";
import { StateMachineDetailPage } from "./pages/StateMachineDetailPage.js";
import { StateMachineExecutionPage } from "./pages/StateMachineExecutionPage.js";
import { CloudWatchPage } from "./pages/CloudWatchPage.js";
import { CloudTrailPage } from "./pages/CloudTrailPage.js";
import { SystemsManagerPage } from "./pages/SystemsManagerPage.js";
import { SecretsManagerPage } from "./pages/SecretsManagerPage.js";
import { KMSKeysPage } from "./pages/KMSKeysPage.js";
import { ACMCertificatesPage } from "./pages/ACMCertificatesPage.js";
import { PrivateCAPage } from "./pages/PrivateCAPage.js";
import { WAFPage } from "./pages/WAFPage.js";
import { BudgetsPage } from "./pages/BudgetsPage.js";
import { NotSupportedServicePage } from "./pages/NotSupportedServicePage.js";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AwsApp>
        <Route path="/ui/" element={<OverviewPage />} />
        <Route path="/ui/ecs" element={<ECSTasksPage />} />
        <Route path="/ui/ecs/services/:cluster/:serviceName" element={<ECSServiceDetailPage />} />
        <Route path="/ui/ecs/:taskArn" element={<ECSTaskDetailPage />} />
        <Route path="/ui/lambda" element={<LambdaFunctionsPage />} />
        <Route path="/ui/lambda/:name" element={<LambdaFunctionDetailPage />} />
        <Route path="/ui/ecr" element={<ECRReposPage />} />
        <Route path="/ui/ecr/:name" element={<ECRRepositoryDetailPage />} />
        <Route path="/ui/s3" element={<S3BucketsPage />} />
        <Route path="/ui/s3/:name" element={<S3BucketDetailPage />} />
        <Route path="/ui/logs" element={<LogGroupsPage />} />
        <Route path="/ui/logs/:name" element={<LogGroupDetailPage />} />
        <Route path="/ui/organizations" element={<OrganizationsPage />} />
        <Route path="/ui/organizations/accounts/:accountId" element={<OrgAccountDetailPage />} />
        <Route path="/ui/iam" element={<IAMUsersPage />} />
        <Route path="/ui/iam/users/:userName" element={<IAMUserSecurityCredentialsPage />} />
        <Route path="/ui/ec2" element={<EC2InstancesPage />} />
        <Route path="/ui/ec2/:instanceId" element={<EC2InstanceDetailPage />} />
        <Route path="/ui/autoscaling" element={<AutoScalingGroupsPage />} />
        <Route path="/ui/batch" element={<BatchPage />} />
        <Route path="/ui/efs" element={<EFSFileSystemsPage />} />
        <Route path="/ui/efs/:fileSystemId" element={<EFSFileSystemDetailPage />} />
        <Route path="/ui/rds" element={<RDSPage />} />
        <Route path="/ui/dynamodb" element={<DynamoDBTablesPage />} />
        <Route path="/ui/dynamodb/:name" element={<DynamoDBTableDetailPage />} />
        <Route path="/ui/elasticache" element={<ElastiCachePage />} />
        <Route path="/ui/vpc" element={<VPCPage />} />
        <Route path="/ui/vpc/:vpcId" element={<VPCDetailPage />} />
        <Route path="/ui/cloudfront" element={<CloudFrontPage />} />
        <Route path="/ui/route53" element={<Route53Page />} />
        <Route path="/ui/route53/:hostedZoneId" element={<Route53HostedZoneDetailPage />} />
        <Route path="/ui/apigateway" element={<APIGatewayPage />} />
        <Route path="/ui/elb" element={<LoadBalancersPage />} />
        <Route path="/ui/cloudmap" element={<CloudMapPage />} />
        <Route path="/ui/codebuild" element={<CodeBuildPage />} />
        <Route path="/ui/amplify" element={<AmplifyPage />} />
        <Route path="/ui/kinesis" element={<KinesisPage />} />
        <Route path="/ui/firehose" element={<FirehosePage />} />
        <Route path="/ui/glue" element={<GluePage />} />
        <Route path="/ui/sns" element={<SNSPage />} />
        <Route path="/ui/sqs" element={<SQSPage />} />
        <Route path="/ui/eventbridge" element={<EventBridgePage />} />
        <Route path="/ui/scheduler" element={<SchedulerPage />} />
        <Route path="/ui/stepfunctions" element={<StepFunctionsPage />} />
        <Route
          path="/ui/stepfunctions/:stateMachineArn/executions/:executionArn"
          element={<StateMachineExecutionPage />}
        />
        <Route path="/ui/stepfunctions/:stateMachineArn" element={<StateMachineDetailPage />} />
        <Route path="/ui/cloudwatch" element={<CloudWatchPage />} />
        <Route path="/ui/cloudtrail" element={<CloudTrailPage />} />
        <Route path="/ui/ssm" element={<SystemsManagerPage />} />
        <Route path="/ui/secretsmanager" element={<SecretsManagerPage />} />
        <Route path="/ui/kms" element={<KMSKeysPage />} />
        <Route path="/ui/acm" element={<ACMCertificatesPage />} />
        <Route path="/ui/private-ca" element={<PrivateCAPage />} />
        <Route path="/ui/waf" element={<WAFPage />} />
        <Route path="/ui/budgets" element={<BudgetsPage />} />
        <Route path="/ui/not-supported/:service" element={<NotSupportedServicePage />} />
        {/* Any other path lands on the overview rather than an empty shell:
            a mistyped or stale deep link must never render a blank console. */}
        <Route path="*" element={<Navigate to="/ui/" replace />} />
      </AwsApp>
    </QueryClientProvider>
  </StrictMode>,
);
