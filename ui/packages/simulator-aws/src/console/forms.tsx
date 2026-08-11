import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import AttributeEditor from "@cloudscape-design/components/attribute-editor";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal } from "./AwsConsole.js";

/**
 * Reusable Cloudscape form building blocks shared by the console's edit and
 * create flows — a key/value editor (the real `AttributeEditor`, which the AWS
 * console itself uses for environment variables and tags) and a tags-editor
 * modal built on it. Both are real Cloudscape components; callers wire the
 * save handler to the real cloud operation.
 */

export interface KeyValueRow {
  key: string;
  value: string;
}

function tagsToRows(tags: Record<string, string>): KeyValueRow[] {
  return Object.entries(tags).map(([key, value]) => ({ key, value }));
}

/** Keys present in `previous` but absent from `next` — the set a set-plus-remove
 * tagging API (Lambda, ECR) must untag, as opposed to S3's whole-set replace. */
export function removedKeys(previous: Record<string, string>, next: Record<string, string>): string[] {
  return Object.keys(previous).filter((key) => !(key in next));
}

export function rowsToTags(rows: KeyValueRow[]): Record<string, string> {
  const tags: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (key) tags[key] = row.value;
  }
  return tags;
}

/** True when every non-blank row has a key and no key repeats — the shape the
 * real cloud APIs accept for a tag/environment set. */
export function rowsAreValid(rows: KeyValueRow[]): boolean {
  const keys = rows.map((row) => row.key.trim()).filter(Boolean);
  return keys.length === new Set(keys).size && rows.every((row) => row.key.trim() !== "" || row.value === "");
}

export function KeyValueEditor({
  rows,
  onChange,
  keyLabel,
  valueLabel,
  addLabel,
  emptyText,
  testIdPrefix,
}: {
  rows: KeyValueRow[];
  onChange: (rows: KeyValueRow[]) => void;
  keyLabel: string;
  valueLabel: string;
  addLabel: string;
  emptyText: string;
  testIdPrefix: string;
}) {
  return (
    <AttributeEditor
      items={rows}
      addButtonText={addLabel}
      removeButtonText="Remove"
      empty={emptyText}
      onAddButtonClick={() => onChange([...rows, { key: "", value: "" }])}
      onRemoveButtonClick={({ detail: { itemIndex } }) => onChange(rows.filter((_, index) => index !== itemIndex))}
      definition={[
        {
          label: keyLabel,
          control: (item: KeyValueRow, index: number) => (
            <Input
              value={item.key}
              ariaLabel={`${keyLabel} ${index + 1}`}
              onChange={(event) =>
                onChange(rows.map((row, i) => (i === index ? { ...row, key: event.detail.value } : row)))
              }
              nativeInputAttributes={{ "data-testid": `${testIdPrefix}-key-${index}` }}
            />
          ),
        },
        {
          label: valueLabel,
          control: (item: KeyValueRow, index: number) => (
            <Input
              value={item.value}
              ariaLabel={`${valueLabel} ${index + 1}`}
              onChange={(event) =>
                onChange(rows.map((row, i) => (i === index ? { ...row, value: event.detail.value } : row)))
              }
              nativeInputAttributes={{ "data-testid": `${testIdPrefix}-value-${index}` }}
            />
          ),
        },
      ]}
      removeButtonAriaLabel={(item) => `Remove ${item.key || "row"}`}
    />
  );
}

/**
 * A tags editor as a Cloudscape modal — the reusable dialog behind every
 * resource's "Manage tags" action. The caller's `save` applies the edited set
 * to the resource through the real cloud operation (Lambda TagResource/
 * UntagResource, ECR TagResource/UntagResource, S3 PutBucketTagging).
 */
export function TagsEditorModal({
  title,
  intro,
  initialTags,
  save,
  onClose,
  onSaved,
  testIdPrefix,
}: {
  title: string;
  intro: string;
  initialTags: Record<string, string>;
  save: (next: Record<string, string>) => Promise<void>;
  onClose: () => void;
  onSaved?: () => void;
  testIdPrefix: string;
}) {
  const [rows, setRows] = useState<KeyValueRow[]>(tagsToRows(initialTags));
  const mutation = useMutation({
    mutationFn: () => save(rowsToTags(rows)),
    onSuccess: () => {
      onSaved?.();
      onClose();
    },
  });
  const valid = rowsAreValid(rows);
  return (
    <AwsModal
      title={title}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid={`${testIdPrefix}-tags-save`}
            disabled={!valid || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending ? "Saving…" : "Save changes"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <p>{intro}</p>
        <FormField label="Tags" description="A key is required and must be unique. Values are optional.">
          <KeyValueEditor
            rows={rows}
            onChange={setRows}
            keyLabel="Key"
            valueLabel="Value"
            addLabel="Add tag"
            emptyText="No tags. Add one to categorize this resource."
            testIdPrefix={`${testIdPrefix}-tag`}
          />
        </FormField>
        {mutation.isError && (
          <AwsErrorAlert>
            <strong>Could not save tags.</strong>{" "}
            {mutation.error instanceof Error ? mutation.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}
