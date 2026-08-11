import { useState } from "react";
import { makeStyles, tokens, Field, Input, Select, Button, Text } from "@fluentui/react-components";
import { AzureIcon } from "../portal/icons.js";
import {
  containerAppTriggerTypes,
  type ContainerAppJobConfigInput,
  type ContainerAppJobContainer,
  type ContainerAppJobDetail,
  type ContainerAppJobEnvVar,
  type CreateContainerAppJobInput,
  type Subscription,
} from "../api.js";

/**
 * The Create and Edit forms for a Container App job — real Fluent inline
 * forms, the same Field/Input/Select shape the other resource forms use. Both
 * build a `ContainerAppJobConfigInput` (the configuration + template a
 * Microsoft.App/jobs PUT carries): replica timeout, parallelism, trigger type,
 * and each container's image and environment variables. The Create form also
 * collects the scoping subscription/resource group, the managed environment,
 * and the job name; the Edit form preserves the job's existing trigger type,
 * retry limit, cron, and container names/commands while editing what the real
 * portal's edit surface edits.
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
    maxWidth: "520px",
  },
  actions: { display: "flex", gap: "8px", marginTop: "4px" },
  containerBlock: {
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: "10px 12px",
    display: "flex",
    flexDirection: "column",
    gap: "8px",
  },
  envRow: { display: "grid", gridTemplateColumns: "1fr 1fr auto", gap: "8px", alignItems: "end" },
});

// Azure Container App job names: 2–32 chars, lowercase letters, digits, and
// hyphens; must start with a letter and end alphanumeric — the same rule the
// real "Create Container App job" blade validates before the request goes out.
const JOB_NAME_PATTERN = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;
export function isValidContainerAppJobName(name: string): boolean {
  return JOB_NAME_PATTERN.test(name);
}

function splitWords(value: string): string[] {
  return value.trim().length ? value.trim().split(/\s+/) : [];
}

// EnvVarsEditor edits a container's environment variables as name/value rows.
function EnvVarsEditor({
  testidPrefix,
  env,
  onChange,
}: {
  testidPrefix: string;
  env: ContainerAppJobEnvVar[];
  onChange: (env: ContainerAppJobEnvVar[]) => void;
}) {
  const styles = useStyles();
  const rows = env.length ? env : [];
  return (
    <div>
      <Text as="span" weight="semibold" size={200} block>
        Environment variables
      </Text>
      {rows.map((row, index) => (
        <div className={styles.envRow} key={index}>
          <Field label="Name">
            <Input
              data-testid={`${testidPrefix}-env-name-${index}`}
              value={row.name}
              onChange={(_, data) => onChange(rows.map((r, i) => (i === index ? { ...r, name: data.value } : r)))}
            />
          </Field>
          <Field label="Value">
            <Input
              data-testid={`${testidPrefix}-env-value-${index}`}
              value={row.value}
              onChange={(_, data) => onChange(rows.map((r, i) => (i === index ? { ...r, value: data.value } : r)))}
            />
          </Field>
          <Button
            type="button"
            appearance="subtle"
            icon={<AzureIcon name="delete" size={16} />}
            aria-label={`Remove environment variable ${row.name || index + 1}`}
            data-testid={`${testidPrefix}-env-remove-${index}`}
            onClick={() => onChange(rows.filter((_, i) => i !== index))}
          />
        </div>
      ))}
      <Button
        type="button"
        appearance="subtle"
        icon={<AzureIcon name="add" size={16} />}
        data-testid={`${testidPrefix}-env-add`}
        onClick={() => onChange([...rows, { name: "", value: "" }])}
      >
        Add environment variable
      </Button>
    </div>
  );
}

// --- Edit form: an existing job's replica timeout, parallelism, and each
// container's image + environment variables, preserving the rest. ---

export interface ContainerAppJobEditFormProps {
  job: ContainerAppJobDetail;
  busy: boolean;
  error?: React.ReactNode;
  onSave: (config: ContainerAppJobConfigInput) => void;
  onDismiss: () => void;
}

export function ContainerAppJobEditForm({ job, busy, error, onSave, onDismiss }: ContainerAppJobEditFormProps) {
  const styles = useStyles();
  const [replicaTimeout, setReplicaTimeout] = useState(String(job.replicaTimeout || 1800));
  const [parallelism, setParallelism] = useState(String(job.parallelism || 1));
  const [containers, setContainers] = useState<ContainerAppJobContainer[]>(() =>
    job.containers.length
      ? job.containers.map((c) => ({ ...c, env: [...c.env] }))
      : [{ name: job.name, image: "", command: [], args: [], env: [] }],
  );

  const setContainer = (index: number, patch: Partial<ContainerAppJobContainer>) =>
    setContainers((current) => current.map((c, i) => (i === index ? { ...c, ...patch } : c)));

  return (
    <form
      className={styles.form}
      data-testid="ca-job-edit-form"
      onSubmit={(event) => {
        event.preventDefault();
        onSave({
          triggerType: job.triggerType || "Manual",
          replicaTimeout: Number(replicaTimeout) || 0,
          replicaRetryLimit: job.replicaRetryLimit,
          parallelism: Number(parallelism) || 0,
          cronExpression: job.cronExpression,
          containers: containers.map((c) => ({ ...c, env: c.env.filter((e) => e.name.trim()) })),
        });
      }}
    >
      <Text as="h2" weight="semibold">
        Edit job configuration
      </Text>
      <Field label="Replica timeout (seconds)">
        <Input
          type="number"
          data-testid="ca-job-edit-timeout"
          value={replicaTimeout}
          onChange={(_, data) => setReplicaTimeout(data.value)}
        />
      </Field>
      <Field label="Parallelism">
        <Input
          type="number"
          data-testid="ca-job-edit-parallelism"
          value={parallelism}
          onChange={(_, data) => setParallelism(data.value)}
        />
      </Field>
      {containers.map((container, index) => (
        <div className={styles.containerBlock} key={index}>
          <Text as="span" weight="semibold" size={200} block>
            Container: {container.name || `#${index + 1}`}
          </Text>
          <Field label="Image">
            <Input
              data-testid={`ca-job-edit-image-${index}`}
              value={container.image}
              onChange={(_, data) => setContainer(index, { image: data.value })}
            />
          </Field>
          <EnvVarsEditor
            testidPrefix={`ca-job-edit-${index}`}
            env={container.env}
            onChange={(env) => setContainer(index, { env })}
          />
        </div>
      ))}
      {error ? (
        <Text as="p" role="alert" data-testid="ca-job-edit-error">
          {error}
        </Text>
      ) : null}
      <div className={styles.actions}>
        <Button type="submit" appearance="primary" data-testid="ca-job-edit-save" disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// --- Create form: a whole new Container App job. ---

export interface ContainerAppJobCreateFormProps {
  subscriptions: Subscription[];
  busy: boolean;
  onCreate: (input: CreateContainerAppJobInput) => void;
  onDismiss: () => void;
}

export function ContainerAppJobCreateForm({ subscriptions, busy, onCreate, onDismiss }: ContainerAppJobCreateFormProps) {
  const styles = useStyles();
  // The subscription list arrives asynchronously, so the chosen value cannot be
  // frozen at mount: a form opened before the query resolves would hold an
  // empty subscription forever and leave its submit permanently disabled.
  // An explicit choice wins; otherwise the first loaded subscription is used.
  const [chosenSubscriptionId, setSubscriptionId] = useState("");
  const subscriptionId = chosenSubscriptionId || subscriptions[0]?.subscriptionId || "";
  const [resourceGroup, setResourceGroup] = useState("sockerless-console");
  const [name, setName] = useState("");
  const [location, setLocation] = useState("eastus");
  const [environmentName, setEnvironmentName] = useState("sockerless-env");
  const [triggerType, setTriggerType] = useState<string>(containerAppTriggerTypes[0]);
  const [replicaTimeout, setReplicaTimeout] = useState("1800");
  const [parallelism, setParallelism] = useState("1");
  const [image, setImage] = useState("");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [env, setEnv] = useState<ContainerAppJobEnvVar[]>([]);

  const trimmedName = name.trim();
  const valid =
    isValidContainerAppJobName(trimmedName) &&
    subscriptionId.trim() !== "" &&
    resourceGroup.trim() !== "" &&
    location.trim() !== "" &&
    environmentName.trim() !== "" &&
    image.trim() !== "";

  return (
    <form
      className={styles.form}
      data-testid="ca-job-create-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!valid) return;
        onCreate({
          subscriptionId: subscriptionId.trim(),
          resourceGroup: resourceGroup.trim(),
          name: trimmedName,
          location: location.trim(),
          environmentName: environmentName.trim(),
          config: {
            triggerType,
            replicaTimeout: Number(replicaTimeout) || 0,
            replicaRetryLimit: 0,
            parallelism: Number(parallelism) || 0,
            cronExpression: "",
            containers: [
              {
                name: trimmedName,
                image: image.trim(),
                command: splitWords(command),
                args: splitWords(args),
                env: env.filter((e) => e.name.trim()),
              },
            ],
          },
        });
      }}
    >
      <Text as="h2" weight="semibold">
        Create Container App job
      </Text>
      <Field label="Subscription">
        <Select
          data-testid="ca-job-create-subscription"
          value={subscriptionId}
          onChange={(event) => setSubscriptionId(event.target.value)}
        >
          <option value="" disabled>
            Select a subscription
          </option>
          {subscriptions.map((subscription) => (
            <option key={subscription.subscriptionId} value={subscription.subscriptionId}>
              {subscription.displayName || subscription.subscriptionId}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Resource group" hint="Created automatically if it doesn't already exist in this subscription.">
        <Input data-testid="ca-job-create-rg" value={resourceGroup} onChange={(_, data) => setResourceGroup(data.value)} />
      </Field>
      <Field label="Job name" hint="2–32 characters. Lowercase letters, numbers, and hyphens. Must start with a letter.">
        <Input data-testid="ca-job-create-name" value={name} onChange={(_, data) => setName(data.value)} />
      </Field>
      <Field label="Region">
        <Input data-testid="ca-job-create-location" value={location} onChange={(_, data) => setLocation(data.value)} />
      </Field>
      <Field label="Container Apps environment" hint="Created automatically if it doesn't already exist.">
        <Input
          data-testid="ca-job-create-env"
          value={environmentName}
          onChange={(_, data) => setEnvironmentName(data.value)}
        />
      </Field>
      <Field label="Trigger type">
        <Select
          data-testid="ca-job-create-trigger"
          value={triggerType}
          onChange={(event) => setTriggerType(event.target.value)}
        >
          {containerAppTriggerTypes.map((trigger) => (
            <option key={trigger} value={trigger}>
              {trigger}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Replica timeout (seconds)">
        <Input
          type="number"
          data-testid="ca-job-create-timeout"
          value={replicaTimeout}
          onChange={(_, data) => setReplicaTimeout(data.value)}
        />
      </Field>
      <Field label="Parallelism">
        <Input
          type="number"
          data-testid="ca-job-create-parallelism"
          value={parallelism}
          onChange={(_, data) => setParallelism(data.value)}
        />
      </Field>
      <Field label="Image">
        <Input data-testid="ca-job-create-image" value={image} onChange={(_, data) => setImage(data.value)} />
      </Field>
      <Field label="Command" hint="Optional. Space-separated; overrides the image entrypoint.">
        <Input data-testid="ca-job-create-command" value={command} onChange={(_, data) => setCommand(data.value)} />
      </Field>
      <Field label="Arguments" hint="Optional. Space-separated arguments passed to the command.">
        <Input data-testid="ca-job-create-args" value={args} onChange={(_, data) => setArgs(data.value)} />
      </Field>
      <EnvVarsEditor testidPrefix="ca-job-create" env={env} onChange={setEnv} />
      <div className={styles.actions}>
        <Button type="submit" appearance="primary" data-testid="ca-job-create-submit" disabled={!valid || busy}>
          {busy ? "Creating…" : "Review + create"}
        </Button>
        <Button type="button" onClick={onDismiss} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
