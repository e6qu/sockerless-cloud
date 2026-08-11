import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  makeStyles,
  tokens,
  Field,
  Input,
  Button,
  Link,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  TableSelectionCell,
  Text,
} from "@fluentui/react-components";
import { AzureCommandBar, AzureEssentials, AzureErrorMessage } from "../portal/AzurePortal.js";
import { AzureTableErrorRow, AzureTableLoadingRow, AzureTableEmptyRow } from "../portal/AzureTable.js";
import {
  createAppRegistration,
  deleteAppRegistration,
  fetchAppRegistrations,
  type AppRegistration,
} from "../api.js";

const useStyles = makeStyles({
  form: {
    backgroundColor: tokens.colorNeutralBackground1,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: "14px 16px",
    margin: "12px 0",
    display: "flex",
    flexDirection: "column",
    gap: "10px",
    maxWidth: "480px",
  },
  formActions: { display: "flex", gap: "8px" },
});

// The Microsoft Entra ID App registrations blade: it lists the directory's
// application registrations from Microsoft Graph and registers new ones the
// way the real portal does — the application object plus its service
// principal — so the appId can immediately authenticate a client_credentials
// grant once a client secret is minted on the Certificates & secrets blade.
export function AppRegistrationsPage() {
  const styles = useStyles();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ["entra-apps"],
    queryFn: fetchAppRegistrations,
  });
  const [registering, setRegistering] = useState(false);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const create = useMutation({
    mutationFn: (displayName: string) => createAppRegistration(displayName),
    onSuccess: (app) => {
      void queryClient.invalidateQueries({ queryKey: ["entra-apps"] });
      setRegistering(false);
      setName("");
      void navigate(`/ui/entra/app-registrations/${app.id}`);
    },
  });

  const remove = useMutation({
    mutationFn: async (objectIds: string[]) => {
      for (const objectId of objectIds) {
        await deleteAppRegistration(objectId);
      }
    },
    onSuccess: () => {
      setSelected(new Set());
      void queryClient.invalidateQueries({ queryKey: ["entra-apps"] });
    },
  });

  const rows = data ?? [];
  const mutationError = create.error ?? remove.error;

  return (
    <>
      <AzureCommandBar
        commands={[
          { label: "New registration", icon: "add", onSelect: () => setRegistering(true) },
          { label: "Refresh", icon: "refresh", onSelect: () => void refetch(), disabled: isFetching },
          {
            label: "Delete",
            icon: "delete",
            disabled: selected.size === 0 || remove.isPending,
            onSelect: () => remove.mutate([...selected]),
          },
          { label: "Feedback", icon: "feedback" },
        ]}
      />
      <div className="az-main">
        <AzureEssentials
          properties={[
            { label: "Directory", value: "Simulator" },
            { label: "App registrations", value: String(rows.length) },
          ]}
        />

        {mutationError ? (
          <AzureErrorMessage testid="entra-apps-error">
            <strong>The directory operation failed.</strong>{" "}
            {mutationError instanceof Error ? mutationError.message : "Microsoft Graph did not respond."}
          </AzureErrorMessage>
        ) : null}

        {registering ? (
          <form
            className={styles.form}
            data-testid="entra-new-registration-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (name.trim()) create.mutate(name.trim());
            }}
          >
            <Text as="h2" weight="semibold">
              Register an application
            </Text>
            <Field label="Name">
              <Input
                data-testid="entra-app-name-input"
                value={name}
                placeholder="Enter a display name for the application"
                onChange={(_, data) => setName(data.value)}
              />
            </Field>
            <div className={styles.formActions}>
              <Button
                type="submit"
                appearance="primary"
                data-testid="entra-register-submit"
                disabled={!name.trim() || create.isPending}
              >
                Register
              </Button>
              <Button type="button" onClick={() => setRegistering(false)}>
                Cancel
              </Button>
            </div>
          </form>
        ) : null}

        <Table aria-label="App registrations" size="small" data-testid="entra-apps-table">
          <TableHeader>
            <TableRow>
              <TableSelectionCell
                type="checkbox"
                checked={rows.length > 0 && rows.every((row) => selected.has(row.id))}
                checkboxIndicator={{
                  "aria-label": "Select all app registrations",
                  disabled: rows.length === 0,
                }}
                onClick={() =>
                  setSelected((current) =>
                    current.size === rows.length ? new Set() : new Set(rows.map((row) => row.id)),
                  )
                }
              />
              <TableHeaderCell>Display name</TableHeaderCell>
              <TableHeaderCell>Application (client) ID</TableHeaderCell>
              <TableHeaderCell>Object ID</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isError ? (
              <AzureTableErrorRow colSpan={4}>
                <strong>Could not load app registrations.</strong>{" "}
                {error instanceof Error ? error.message : "Microsoft Graph did not respond."}
              </AzureTableErrorRow>
            ) : isLoading ? (
              <AzureTableLoadingRow colSpan={4} label="Loading app registrations…" />
            ) : rows.length === 0 ? (
              <AzureTableEmptyRow
                colSpan={4}
                title="No app registrations to display"
                description="Applications registered in this directory appear here."
              />
            ) : (
              rows.map((row: AppRegistration) => (
                <TableRow key={row.id} data-testid="entra-app-row">
                  <TableSelectionCell
                    type="checkbox"
                    checked={selected.has(row.id)}
                    checkboxIndicator={{ "aria-label": `Select ${row.displayName}` }}
                    onClick={() =>
                      setSelected((current) => {
                        const next = new Set(current);
                        if (next.has(row.id)) next.delete(row.id);
                        else next.add(row.id);
                        return next;
                      })
                    }
                  />
                  <TableCell>
                    <Link
                      href={`/ui/entra/app-registrations/${row.id}`}
                      onClick={(event) => {
                        event.preventDefault();
                        navigate(`/ui/entra/app-registrations/${row.id}`);
                      }}
                    >
                      {row.displayName}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <code>{row.appId}</code>
                  </TableCell>
                  <TableCell>
                    <code>{row.id}</code>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </>
  );
}
