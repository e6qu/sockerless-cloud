import { useState } from "react";
import {
  makeStyles,
  tokens,
  Field,
  Input,
  Select,
  Button,
  Table,
  TableHeader,
  TableRow,
  TableHeaderCell,
  TableBody,
  TableCell,
  Text,
} from "@fluentui/react-components";
import { AzureIcon } from "../portal/icons.js";
import { AzureWarningMessage, AzureErrorMessage } from "../portal/AzurePortal.js";
import { AzureTableEmptyRow } from "../portal/AzureTable.js";
import type { ClientSecretMetadata, MintedClientSecret } from "../api.js";

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
  hint: { display: "block", maxWidth: "72ch", marginBottom: "10px" },
  secretValue: { display: "inline-flex", alignItems: "center", gap: "6px" },
});

// The expiry choices the real Certificates & secrets blade offers, as days
// from now; the blade posts the resulting endDateTime to Microsoft Graph.
export const EXPIRY_OPTIONS = [
  { label: "90 days (3 months)", days: 90 },
  { label: "Recommended: 180 days (6 months)", days: 180 },
  { label: "365 days (12 months)", days: 365 },
  { label: "730 days (24 months)", days: 730 },
] as const;

export interface CertificatesAndSecretsProps {
  credentials: ClientSecretMetadata[];
  /** The secret minted in this session, shown in full exactly once. */
  minted: MintedClientSecret | null;
  busy: boolean;
  onCreate: (description: string, endDateTime: string) => void;
  onRemove: (keyId: string) => void;
}

function expiryDate(days: number): string {
  const end = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return end.toISOString();
}

function formatDate(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleDateString();
}

// CertificatesAndSecrets is the Certificates & secrets blade of an app
// registration: it lists the client secrets' metadata (Microsoft Graph never
// returns a stored secret's value — only its hint) and mints new ones. The
// full secretText appears exactly once, from the addPassword response held in
// this session; leaving the blade discards it, matching the real portal.
export function CertificatesAndSecrets({ credentials, minted, busy, onCreate, onRemove }: CertificatesAndSecretsProps) {
  const styles = useStyles();
  const [adding, setAdding] = useState(false);
  const [description, setDescription] = useState("");
  const [days, setDays] = useState<number>(180);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);

  return (
    <section className="az-blade-section" aria-label="Certificates & secrets">
      <Text as="h2" weight="semibold" block>
        Certificates &amp; secrets
      </Text>
      <Text as="p" className={styles.hint}>
        Credentials enable confidential applications to identify themselves to the authentication service when
        receiving tokens. A client secret is a string the application uses as a password.
      </Text>

      <Button
        appearance="subtle"
        icon={<AzureIcon name="add" size={16} />}
        data-testid="entra-new-client-secret"
        disabled={busy}
        onClick={() => setAdding(true)}
      >
        New client secret
      </Button>

      {adding ? (
        <form
          className={styles.form}
          data-testid="entra-secret-form"
          onSubmit={(event) => {
            event.preventDefault();
            setAdding(false);
            onCreate(description.trim(), expiryDate(days));
            setDescription("");
          }}
        >
          <Text as="h3" weight="semibold">
            Add a client secret
          </Text>
          <Field label="Description">
            <Input
              data-testid="entra-secret-description"
              value={description}
              placeholder="Enter a description for this client secret"
              onChange={(_, data) => setDescription(data.value)}
            />
          </Field>
          <Field label="Expires">
            <Select
              data-testid="entra-secret-expiry"
              value={days}
              onChange={(event) => setDays(Number(event.target.value))}
            >
              {EXPIRY_OPTIONS.map((option) => (
                <option key={option.days} value={option.days}>
                  {option.label}
                </option>
              ))}
            </Select>
          </Field>
          <div className={styles.formActions}>
            <Button type="submit" appearance="primary" data-testid="entra-secret-add" disabled={busy}>
              Add
            </Button>
            <Button type="button" onClick={() => setAdding(false)}>
              Cancel
            </Button>
          </div>
        </form>
      ) : null}

      {minted ? (
        <AzureWarningMessage testid="entra-secret-notice">
          Remember to copy the new client secret value. You won&rsquo;t be able to retrieve it after you leave this
          blade.
        </AzureWarningMessage>
      ) : null}
      {copyError ? (
        <AzureErrorMessage>
          <strong>Could not copy the secret to the clipboard.</strong> {copyError}
        </AzureErrorMessage>
      ) : null}

      <Table aria-label="Certificates & secrets" size="small" data-testid="entra-secrets-table">
        <TableHeader>
          <TableRow>
            <TableHeaderCell>Description</TableHeaderCell>
            <TableHeaderCell>Expires</TableHeaderCell>
            <TableHeaderCell>Value</TableHeaderCell>
            <TableHeaderCell>Secret ID</TableHeaderCell>
            <TableHeaderCell aria-label="Actions" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {credentials.length === 0 ? (
            <AzureTableEmptyRow colSpan={5} title="No client secrets have been created for this application." />
          ) : (
            credentials.map((credential) => {
              const isMinted = minted !== null && credential.keyId === minted.keyId;
              return (
                <TableRow key={credential.keyId} data-testid="entra-secret-row">
                  <TableCell>{credential.displayName || "—"}</TableCell>
                  <TableCell>{formatDate(credential.endDateTime)}</TableCell>
                  <TableCell>
                    {isMinted ? (
                      <span className={styles.secretValue}>
                        <code data-testid="entra-secret-value">{minted.secretText}</code>
                        <Button
                          appearance="transparent"
                          icon={<AzureIcon name="copy" size={16} />}
                          aria-label="Copy the client secret value"
                          data-testid="entra-secret-copy"
                          onClick={() => {
                            navigator.clipboard.writeText(minted.secretText).then(
                              () => setCopied(true),
                              (err: unknown) => setCopyError(err instanceof Error ? err.message : String(err)),
                            );
                          }}
                        />
                        {copied ? <span role="status">Copied</span> : null}
                      </span>
                    ) : (
                      <code data-testid="entra-secret-hint">{credential.hint ? `${credential.hint}${"*".repeat(31)}` : "Hidden"}</code>
                    )}
                  </TableCell>
                  <TableCell>
                    <code>{credential.keyId}</code>
                  </TableCell>
                  <TableCell>
                    <Button
                      appearance="transparent"
                      icon={<AzureIcon name="delete" size={16} />}
                      aria-label={`Delete the client secret ${credential.displayName || credential.keyId}`}
                      data-testid="entra-secret-delete"
                      disabled={busy}
                      onClick={() => onRemove(credential.keyId)}
                    />
                  </TableCell>
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
    </section>
  );
}
