import { useState } from "react";
import { makeStyles, tokens, Field, Input, Button, Text } from "@fluentui/react-components";
import { AzureIcon } from "./icons.js";
import { AzureErrorMessage } from "./AzurePortal.js";
import type { ResourceTags } from "../api.js";

/**
 * The reusable Tags editor every resource detail blade shares — the real
 * portal's "Tags" blade, which reads and writes the resource's ARM `tags` map
 * through a PATCH. It is a real Fluent inline form (the same
 * Field/Input/Button shape the Create forms use), rendered when a blade's
 * "Edit tags" command opens it. Each tag is one editable name/value row with a
 * per-row remove control; a blank row can be added. On save it drops rows with
 * an empty name and hands the caller the resulting `tags` map, which the
 * caller writes with `updateResourceTags` (or the Function App's
 * httpsOnly-preserving `updateFunctionAppTags`).
 */

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
    maxWidth: "560px",
  },
  hint: { display: "block", maxWidth: "72ch" },
  row: { display: "grid", gridTemplateColumns: "1fr 1fr auto", gap: "8px", alignItems: "end" },
  actions: { display: "flex", gap: "8px", marginTop: "4px" },
});

interface TagRow {
  key: string;
  value: string;
}

export interface TagsEditorProps {
  tags: ResourceTags;
  busy: boolean;
  error?: React.ReactNode;
  /** Base test id for the form and its controls, e.g. "acr-tags". */
  testidPrefix: string;
  onSave: (tags: ResourceTags) => void;
  onDismiss: () => void;
}

function rowsFromTags(tags: ResourceTags): TagRow[] {
  const rows = Object.entries(tags).map(([key, value]) => ({ key, value }));
  return rows.length > 0 ? rows : [{ key: "", value: "" }];
}

export function TagsEditor({ tags, busy, error, testidPrefix, onSave, onDismiss }: TagsEditorProps) {
  const styles = useStyles();
  const [rows, setRows] = useState<TagRow[]>(() => rowsFromTags(tags));

  const setRow = (index: number, patch: Partial<TagRow>) =>
    setRows((current) => current.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  const collect = (): ResourceTags => {
    const result: ResourceTags = {};
    for (const row of rows) {
      const key = row.key.trim();
      if (key) result[key] = row.value;
    }
    return result;
  };

  return (
    <form
      className={styles.form}
      data-testid={`${testidPrefix}-form`}
      onSubmit={(event) => {
        event.preventDefault();
        onSave(collect());
      }}
    >
      <Text as="h2" weight="semibold">
        Tags
      </Text>
      <Text as="p" className={styles.hint}>
        Tags are name/value pairs that let you categorize resources and view consolidated billing. A resource can
        have up to 50 tags.
      </Text>
      {rows.map((row, index) => (
        <div className={styles.row} key={index}>
          <Field label="Name">
            <Input
              data-testid={`${testidPrefix}-key-${index}`}
              value={row.key}
              onChange={(_, data) => setRow(index, { key: data.value })}
            />
          </Field>
          <Field label="Value">
            <Input
              data-testid={`${testidPrefix}-value-${index}`}
              value={row.value}
              onChange={(_, data) => setRow(index, { value: data.value })}
            />
          </Field>
          <Button
            type="button"
            appearance="subtle"
            icon={<AzureIcon name="delete" size={16} />}
            aria-label={`Remove tag ${row.key || index + 1}`}
            data-testid={`${testidPrefix}-remove-${index}`}
            disabled={busy}
            onClick={() => setRows((current) => (current.length === 1 ? [{ key: "", value: "" }] : current.filter((_, i) => i !== index)))}
          />
        </div>
      ))}
      <Button
        type="button"
        appearance="subtle"
        icon={<AzureIcon name="add" size={16} />}
        data-testid={`${testidPrefix}-add`}
        disabled={busy}
        onClick={() => setRows((current) => [...current, { key: "", value: "" }])}
      >
        Add a tag
      </Button>
      {error ? <AzureErrorMessage testid={`${testidPrefix}-error`}>{error}</AzureErrorMessage> : null}
      <div className={styles.actions}>
        <Button type="submit" appearance="primary" data-testid={`${testidPrefix}-save`} disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
