import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import {
  AwsButton,
  AwsContainer,
  AwsCopyButton,
  AwsEmptyState,
  AwsErrorAlert,
  AwsKeyValue,
  AwsPageHeader,
  AwsStatus,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import { fetchOrgAccount } from "../api.js";
import { AccountActionModal, orgAccountStatusKind } from "./OrganizationsPage.js";

// An AWS Organizations member account, read with DescribeAccount — the detail
// the real console opens from the accounts list, with the same two actions:
// Remove from organization and Close.

export function OrgAccountDetailPage() {
  const { accountId = "" } = useParams();
  const navigate = useNavigate();
  const account = useQuery({
    queryKey: ["org-account", accountId],
    queryFn: () => fetchOrgAccount(accountId),
  });
  const [action, setAction] = useState<"remove" | "close" | null>(null);

  return (
    <>
      <AwsPageHeader
        title={account.data?.name ?? accountId}
        description="Member account in AWS Organizations."
        actions={
          <>
            <AwsButton
              data-testid="org-remove-account"
              disabled={!account.isSuccess}
              onClick={() => setAction("remove")}
            >
              Remove from organization
            </AwsButton>
            <AwsButton data-testid="org-close-account" disabled={!account.isSuccess} onClick={() => setAction("close")}>
              Close account
            </AwsButton>
          </>
        }
      />
      <AwsContainer>
        {account.isError ? (
          <AwsErrorAlert testId="org-error">
            <strong>Could not load the account.</strong>{" "}
            {account.error instanceof Error ? account.error.message : "The request failed."}
          </AwsErrorAlert>
        ) : account.isLoading ? (
          <AwsEmptyState title="Loading account…" loading />
        ) : account.data ? (
          <div data-testid="org-account-detail">
            <AwsKeyValue
              items={[
                {
                  label: "Account ID",
                  value: (
                    <>
                      <code>{account.data.id}</code>
                      <AwsCopyButton value={account.data.id} label="Copy account ID" />
                    </>
                  ),
                },
                {
                  label: "ARN",
                  value: (
                    <>
                      <code>{account.data.arn}</code>
                      <AwsCopyButton value={account.data.arn} label="Copy account ARN" />
                    </>
                  ),
                },
                { label: "Account name", value: account.data.name },
                { label: "Email", value: account.data.email },
                {
                  label: "Status",
                  value: <AwsStatus status={account.data.status} kind={orgAccountStatusKind(account.data.status)} />,
                },
                { label: "Joined method", value: account.data.joinedMethod },
                { label: "Joined", value: formatEpoch(account.data.joinedTimestamp) },
              ]}
            />
          </div>
        ) : null}
      </AwsContainer>
      {action && account.data && (
        <AccountActionModal
          action={action}
          accounts={[{ id: account.data.id, name: account.data.name }]}
          clearSelection={() => navigate("/ui/organizations")}
          onClose={() => setAction(null)}
        />
      )}
    </>
  );
}
