import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import {
  AwsButton,
  AwsErrorAlert,
  AwsModal,
  AwsResourceTable,
  AwsStatus,
  type AwsColumn,
} from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createFirehoseDeliveryStream,
  deleteFirehoseDeliveryStream,
  fetchFirehoseDeliveryStreams,
  putFirehoseRecord,
  setFirehoseEncryption,
  type FirehoseDeliveryStream,
} from "../api.js";

const columns: AwsColumn<FirehoseDeliveryStream>[] = [
  { id: "name", header: "Delivery stream", cell: (row) => row.name, value: (row) => row.name },
  { id: "status", header: "Status", cell: (row) => <AwsStatus status={row.status} />, value: (row) => row.status },
  { id: "source", header: "Source", cell: (row) => row.type, value: (row) => row.type },
  { id: "destination", header: "Destination", cell: (row) => row.bucketArn, value: (row) => row.bucketArn },
  {
    id: "encryption",
    header: "Server-side encryption",
    cell: (row) => row.encryptionStatus,
    value: (row) => row.encryptionStatus,
  },
  {
    id: "created",
    header: "Created",
    cell: (row) => formatEpoch(row.createdAt),
    value: (row) => String(row.createdAt),
  },
];

function CreateDeliveryStreamModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [bucketArn, setBucketArn] = useState("");
  const [roleArn, setRoleArn] = useState("");
  const [prefix, setPrefix] = useState("");
  const [compression, setCompression] = useState<"UNCOMPRESSED" | "GZIP" | "ZIP">("UNCOMPRESSED");
  const create = useMutation({
    mutationFn: () => createFirehoseDeliveryStream({
      name,
      bucketArn,
      roleArn,
      prefix,
      compressionFormat: compression,
    }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["firehose-delivery-streams"] });
      onClose();
    },
  });
  return (
    <AwsModal
      title="Create Firehose stream"
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Cancel</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="firehose-create-submit"
            disabled={!name || !bucketArn || !roleArn || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating…" : "Create delivery stream"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField label="Delivery stream name">
          <Input value={name} onChange={(event) => setName(event.detail.value)} />
        </FormField>
        <FormField label="Amazon S3 bucket ARN" description="The destination bucket must already exist.">
          <Input value={bucketArn} onChange={(event) => setBucketArn(event.detail.value)} />
        </FormField>
        <FormField
          label="IAM role ARN"
          description="Firehose assumes this role to write records to the destination bucket."
        >
          <Input value={roleArn} onChange={(event) => setRoleArn(event.detail.value)} />
        </FormField>
        <FormField label="S3 prefix">
          <Input value={prefix} onChange={(event) => setPrefix(event.detail.value)} />
        </FormField>
        <FormField label="Compression">
          <Select
            selectedOption={{ label: compression, value: compression }}
            options={["UNCOMPRESSED", "GZIP", "ZIP"].map((value) => ({ label: value, value }))}
            onChange={(event) => setCompression(
              (event.detail.selectedOption.value ?? "UNCOMPRESSED") as typeof compression,
            )}
          />
        </FormField>
        {create.isError && (
          <AwsErrorAlert>
            <strong>Could not create the delivery stream.</strong>{" "}
            {create.error instanceof Error ? create.error.message : "The request failed."}
          </AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

function SendRecordModal({ stream, onClose }: { stream: FirehoseDeliveryStream; onClose: () => void }) {
  const [data, setData] = useState('{"source":"aws-console"}\n');
  const [recordId, setRecordId] = useState("");
  const send = useMutation({
    mutationFn: () => putFirehoseRecord(stream.name, data),
    onSuccess: setRecordId,
  });
  return (
    <AwsModal
      title={`Send test record to ${stream.name}`}
      onDismiss={onClose}
      footer={
        <>
          <AwsButton onClick={onClose}>Close</AwsButton>
          <AwsButton
            variant="primary"
            data-testid="firehose-send-record-submit"
            disabled={!data || send.isPending}
            onClick={() => send.mutate()}
          >
            {send.isPending ? "Sending…" : "Send record"}
          </AwsButton>
        </>
      }
    >
      <SpaceBetween size="m">
        <FormField label="Record data" description="Firehose delivers these UTF-8 bytes to the configured destination.">
          <Textarea value={data} rows={8} onChange={(event) => setData(event.detail.value)} />
        </FormField>
        {recordId && <p>Accepted record ID: <code>{recordId}</code></p>}
        {send.isError && (
          <AwsErrorAlert>{send.error instanceof Error ? send.error.message : "The request failed."}</AwsErrorAlert>
        )}
      </SpaceBetween>
    </AwsModal>
  );
}

export function FirehosePage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [sending, setSending] = useState<FirehoseDeliveryStream | null>(null);
  const mutate = useMutation({
    mutationFn: async ({ streams, action }: {
      streams: FirehoseDeliveryStream[];
      action: "delete" | "encrypt" | "decrypt";
    }) => {
      for (const stream of streams) {
        if (action === "delete") await deleteFirehoseDeliveryStream(stream.name);
        else await setFirehoseEncryption(stream.name, action === "encrypt");
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["firehose-delivery-streams"] }),
  });
  return (
    <>
      <AwsResourceTable<FirehoseDeliveryStream>
        title="Delivery streams"
        description="Amazon Data Firehose streams in this account and Region."
        columns={columns}
        queryKey={["firehose-delivery-streams"]}
        queryFn={fetchFirehoseDeliveryStreams}
        filterPlaceholder="Find delivery streams"
        emptyTitle="No delivery streams"
        emptyDescription="Create a Firehose stream to deliver streaming data to Amazon S3."
        rowKey={(row) => row.arn}
        tableTestId="firehose-table"
        errorTestId="firehose-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length !== 1} onClick={() => setSending(selected[0] ?? null)}>
              Send test data
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate({ streams: selected, action: "encrypt" })}
            >
              Enable encryption
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate({ streams: selected, action: "decrypt" })}
            >
              Disable encryption
            </AwsButton>
            <AwsButton
              disabled={selected.length === 0 || mutate.isPending}
              onClick={() => mutate.mutate(
                { streams: selected, action: "delete" },
                { onSuccess: clearSelection },
              )}
            >
              Delete
            </AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>
              {isFetching ? "Refreshing…" : "Refresh"}
            </AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create delivery stream</AwsButton>
          </>
        )}
      />
      {creating && <CreateDeliveryStreamModal onClose={() => setCreating(false)} />}
      {sending && <SendRecordModal stream={sending} onClose={() => setSending(null)} />}
    </>
  );
}
