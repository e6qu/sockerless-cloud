import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Select from "@cloudscape-design/components/select";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { deleteWAFWebACL, fetchWAFIPSets, fetchWAFWebACLs, type WAFIPSet, type WAFScope, type WAFWebACL } from "../api.js";

// AWS WAF — Web ACLs and IP sets. Every WAF read takes a Scope: regional
// resources and CloudFront distributions are separate namespaces, so the real
// console makes an operator choose one, and so does this page. ListWebACLs,
// ListIPSets, and DeleteWebACL are the real WAF operations behind it.

const SCOPES: { label: string; value: WAFScope }[] = [
  { label: "Regional resources", value: "REGIONAL" },
  { label: "CloudFront distributions", value: "CLOUDFRONT" },
];

const webACLColumns: AwsColumn<WAFWebACL>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "Web ACL ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
  { id: "arn", header: "ARN", cell: (row) => row.arn, value: (row) => row.arn },
];

const ipSetColumns: AwsColumn<WAFIPSet>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  { id: "id", header: "IP set ID", cell: (row) => row.id, value: (row) => row.id },
  { id: "description", header: "Description", cell: (row) => row.description || "–", value: (row) => row.description },
];

function DeleteWebACLsModal({
  acls,
  scope,
  onClose,
  clearSelection,
}: {
  acls: WAFWebACL[];
  scope: WAFScope;
  onClose: () => void;
  clearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => {
      for (const acl of acls) {
        await deleteWAFWebACL(acl, scope);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["waf-web-acls", scope] }),
    onSuccess: () => {
      clearSelection();
      onClose();
    },
  });
  return (
    <AwsModal
      title={acls.length === 1 ? `Delete ${acls[0].name}?` : `Delete ${acls.length} web ACLs?`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="waf-delete-web-acl-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AwsButton>
        </>
      }
    >
      <p>A web ACL still associated with a resource must be disassociated before WAF will delete it.</p>
      <ul>
        {acls.map((acl) => (
          <li key={acl.id}>
            <code>{acl.name}</code>
          </li>
        ))}
      </ul>
      {remove.isError && (
        <AwsErrorAlert>
          <strong>Could not delete.</strong>{" "}
          {remove.error instanceof Error ? remove.error.message : "The request failed."}
        </AwsErrorAlert>
      )}
    </AwsModal>
  );
}

export function WAFPage() {
  const [scope, setScope] = useState<WAFScope>("REGIONAL");
  const [deleting, setDeleting] = useState<{ acls: WAFWebACL[]; clearSelection: () => void } | null>(null);
  const selectedScope = SCOPES.find((option) => option.value === scope) ?? SCOPES[0];
  const scopeSelect = (
    <div style={{ minWidth: 240 }}>
      <Select
        selectedOption={selectedScope}
        options={SCOPES}
        ariaLabel="Resource type"
        onChange={(event) => setScope((event.detail.selectedOption.value as WAFScope) ?? "REGIONAL")}
        data-testid="waf-scope-select"
      />
    </div>
  );
  return (
    <>
      <AwsResourceTable<WAFWebACL>
        title="Web ACLs"
        description="AWS WAF web ACLs for the selected resource type."
        columns={webACLColumns}
        queryKey={["waf-web-acls", scope]}
        queryFn={() => fetchWAFWebACLs(scope)}
        filterPlaceholder="Find web ACLs"
        emptyTitle="No web ACLs"
        emptyDescription="No AWS WAF web ACLs exist for the selected resource type."
        rowKey={(row) => row.id}
        tableTestId="waf-web-acls-table"
        errorTestId="waf-web-acls-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            {scopeSelect}
            <AwsButton
              data-testid="waf-delete-web-acl"
              disabled={selected.length === 0}
              onClick={() => setDeleting({ acls: selected, clearSelection })}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
          </>
        )}
      />
      <AwsResourceTable<WAFIPSet>
        title="IP sets"
        headingVariant="h2"
        description="AWS WAF IP sets for the selected resource type."
        columns={ipSetColumns}
        queryKey={["waf-ip-sets", scope]}
        queryFn={() => fetchWAFIPSets(scope)}
        filterPlaceholder="Find IP sets"
        emptyTitle="No IP sets"
        emptyDescription="No AWS WAF IP sets exist for the selected resource type."
        rowKey={(row) => row.id}
        tableTestId="waf-ip-sets-table"
        errorTestId="waf-ip-sets-error"
        actions={({ refetch, isFetching }) => (
          <AwsButton onClick={refetch} disabled={isFetching}>
            {isFetching ? "Refreshing…" : "Refresh"}
          </AwsButton>
        )}
      />
      {deleting && (
        <DeleteWebACLsModal
          acls={deleting.acls}
          scope={scope}
          clearSelection={deleting.clearSelection}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
