import { useState } from "react";
import { useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { makeStyles, tokens, Text } from "@fluentui/react-components";
import { AzureCommandBar, AzureEssentials, AzureErrorMessage, AzureEmptyState } from "../portal/AzurePortal.js";
import { directoryTenantId } from "../portal/federation.js";
import {
  addClientSecret,
  fetchAppRegistration,
  removeClientSecret,
  subscriptionIds,
  type MintedClientSecret,
} from "../api.js";
import { CertificatesAndSecrets } from "./CertificatesAndSecrets.js";

const useStyles = makeStyles({
  code: {
    backgroundColor: tokens.colorNeutralBackground1,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: "12px",
    fontSize: "12px",
    fontFamily: tokens.fontFamilyMonospace,
    overflowX: "auto",
    color: tokens.colorNeutralForeground1,
  },
  hint: { display: "block", maxWidth: "72ch", marginBottom: "10px" },
});

// CliUsage shows credential usage that genuinely works against this
// deployment: the OAuth2 client_credentials token request (the same request
// `az login --service-principal` sends) followed by an az CLI read of Azure
// Resource Manager with the issued token — the exact flow the simulator's CLI
// test suite validates. Against a TLS-served Microsoft Entra endpoint the
// az login form itself applies, so both are shown.
function CliUsage({
  appId,
  tenant,
  subscription,
  origin,
}: {
  appId: string;
  tenant: string | null;
  subscription: string | null;
  origin: string;
}) {
  const styles = useStyles();
  const tenantId = tenant ?? "<tenant-id>";
  const subscriptionId = subscription ?? "<subscription-id>";
  return (
    <section className="az-blade-section" aria-label="Connect with the Azure CLI">
      <Text as="h2" weight="semibold" block>
        Connect with the Azure CLI
      </Text>
      <Text as="p" className={styles.hint}>
        Use the application (client) ID with a client secret from this blade. The token request below is the request{" "}
        <code>az login --service-principal</code> sends.
      </Text>
      <pre className={styles.code} data-testid="entra-cli-usage">
        {`# Acquire an Azure Resource Manager token with the client secret
TOKEN=$(curl -s -X POST "${origin}/${tenantId}/oauth2/v2.0/token" \\
  -d grant_type=client_credentials \\
  -d client_id=${appId} \\
  -d "client_secret=<client-secret-value>" \\
  -d scope=https://management.azure.com/.default | jq -r .access_token)

# Read Azure Resource Manager as the service principal
az rest --method get \\
  --url "${origin}/subscriptions/${subscriptionId}/resourcegroups?api-version=2021-04-01" \\
  --headers "Authorization=Bearer $TOKEN"

# Against a TLS-served Microsoft Entra endpoint, sign the CLI in directly:
az login --service-principal --username ${appId} \\
  --password "<client-secret-value>" --tenant ${tenantId}`}
      </pre>
      <Text as="p" className={styles.hint}>
        Azure SDK equivalent (the client-credentials flow):{" "}
        <code>
          azidentity.NewClientSecretCredential(&quot;{tenantId}&quot;, &quot;{appId}&quot;,
          &quot;&lt;client-secret-value&gt;&quot;, nil)
        </code>{" "}
        — azidentity requires an https authority host; over plain http issue the token request above.
      </Text>
    </section>
  );
}

// The app registration blade: the Overview essentials (application/client ID,
// directory/tenant ID, object ID) above the Certificates & secrets blade and
// the CLI usage for the minted material — all read and written through the
// real Microsoft Graph surface with Graph-scoped federated credentials.
export function AppRegistrationDetailPage() {
  const { objectId = "" } = useParams();
  const queryClient = useQueryClient();
  const [minted, setMinted] = useState<MintedClientSecret | null>(null);

  const { data: app, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ["entra-app", objectId],
    queryFn: () => fetchAppRegistration(objectId),
  });
  const { data: tenant } = useQuery({ queryKey: ["entra-tenant"], queryFn: directoryTenantId });
  const { data: subscriptions } = useQuery({ queryKey: ["subscriptions"], queryFn: subscriptionIds });

  const create = useMutation({
    mutationFn: ({ description, endDateTime }: { description: string; endDateTime: string }) =>
      addClientSecret(objectId, description, endDateTime),
    onSuccess: (credential) => {
      setMinted(credential);
      void queryClient.invalidateQueries({ queryKey: ["entra-app", objectId] });
    },
  });

  const remove = useMutation({
    mutationFn: (keyId: string) => removeClientSecret(objectId, keyId),
    onSuccess: (_, keyId) => {
      setMinted((current) => (current?.keyId === keyId ? null : current));
      void queryClient.invalidateQueries({ queryKey: ["entra-app", objectId] });
    },
  });

  const mutationError = create.error ?? remove.error;

  return (
    <>
      <AzureCommandBar
        commands={[
          { label: "Refresh", icon: "refresh", onSelect: () => void refetch(), disabled: isFetching },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      <div className="az-main">
        {isError ? (
          <AzureErrorMessage testid="entra-app-error">
            <strong>Could not load the app registration.</strong>{" "}
            {error instanceof Error ? error.message : "Microsoft Graph did not respond."}
          </AzureErrorMessage>
        ) : isLoading || !app ? (
          <AzureEmptyState title="Loading the app registration…" loading />
        ) : (
          <>
            <Text as="h2" weight="semibold" block data-testid="entra-app-name">
              {app.displayName}
            </Text>
            <AzureEssentials
              properties={[
                { label: "Display name", value: app.displayName },
                {
                  label: "Application (client) ID",
                  value: <code data-testid="entra-app-client-id">{app.appId}</code>,
                },
                {
                  label: "Directory (tenant) ID",
                  value: <code data-testid="entra-app-tenant-id">{tenant ?? "—"}</code>,
                },
                { label: "Object ID", value: <code>{app.id}</code> },
                { label: "Supported account types", value: app.signInAudience || "My organization only" },
              ]}
            />

            {mutationError ? (
              <AzureErrorMessage testid="entra-secret-error">
                <strong>The credential operation failed.</strong>{" "}
                {mutationError instanceof Error ? mutationError.message : "Microsoft Graph did not respond."}
              </AzureErrorMessage>
            ) : null}

            <CertificatesAndSecrets
              credentials={app.passwordCredentials}
              minted={minted}
              busy={create.isPending || remove.isPending}
              onCreate={(description, endDateTime) => create.mutate({ description, endDateTime })}
              onRemove={(keyId) => remove.mutate(keyId)}
            />

            <CliUsage
              appId={app.appId}
              tenant={tenant ?? null}
              subscription={subscriptions?.[0] ?? null}
              origin={window.location.origin}
            />
          </>
        )}
      </div>
    </>
  );
}
