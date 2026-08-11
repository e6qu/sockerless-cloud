import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Input from "@cloudscape-design/components/input";
import FormField from "@cloudscape-design/components/form-field";
import Checkbox from "@cloudscape-design/components/checkbox";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { AwsButton, AwsErrorAlert, AwsModal, AwsResourceTable, type AwsColumn } from "../console/index.js";
import { formatEpoch } from "../console/format.js";
import {
  createCloudTrailTrail,
  createS3Bucket,
  deleteCloudTrailTrail,
  fetchCloudTrailEvents,
  fetchCloudTrailTrails,
  startCloudTrailLogging,
  stopCloudTrailLogging,
  type CloudTrailEvent,
  type CloudTrailTrail,
} from "../api.js";

// AWS CloudTrail — Trails and Event history, the two surfaces the real console
// leads with. DescribeTrails and LookupEvents on the real CloudTrail API
// (X-Amz-Target CloudTrail_20131101.<Op>).

const trailColumns: AwsColumn<CloudTrailTrail>[] = [
  { id: "name", header: "Name", cell: (row) => row.name, value: (row) => row.name },
  {
    id: "isLogging",
    header: "Logging",
    cell: (row) => (row.isLogging ? "On" : "Off"),
    value: (row) => (row.isLogging ? "On" : "Off"),
  },
  { id: "homeRegion", header: "Home Region", cell: (row) => row.homeRegion, value: (row) => row.homeRegion },
  {
    id: "isMultiRegionTrail",
    header: "Multi-Region trail",
    cell: (row) => (row.isMultiRegionTrail ? "Yes" : "No"),
    value: (row) => (row.isMultiRegionTrail ? "Yes" : "No"),
  },
  { id: "s3BucketName", header: "S3 bucket", cell: (row) => row.s3BucketName || "–", value: (row) => row.s3BucketName },
  {
    id: "logFileValidationEnabled",
    header: "Log file validation",
    cell: (row) => (row.logFileValidationEnabled ? "Enabled" : "Disabled"),
    value: (row) => (row.logFileValidationEnabled ? "Enabled" : "Disabled"),
  },
];

const eventColumns: AwsColumn<CloudTrailEvent>[] = [
  { id: "eventName", header: "Event name", cell: (row) => row.eventName, value: (row) => row.eventName },
  {
    id: "eventTime",
    header: "Event time",
    cell: (row) => formatEpoch(row.eventTime),
    value: (row) => String(row.eventTime),
  },
  { id: "username", header: "User name", cell: (row) => row.username || "–", value: (row) => row.username },
  { id: "eventSource", header: "Event source", cell: (row) => row.eventSource, value: (row) => row.eventSource },
  { id: "readOnly", header: "Read-only", cell: (row) => row.readOnly || "–", value: (row) => row.readOnly },
];

function CreateTrailModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [bucket, setBucket] = useState("");
  const [createBucket, setCreateBucket] = useState(true);
  const create = useMutation({
    mutationFn: async () => {
      if (createBucket) await createS3Bucket(bucket);
      await createCloudTrailTrail(name, bucket);
      await startCloudTrailLogging(name);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["cloudtrail-trails"] });
      onClose();
    },
  });
  return (
    <AwsModal title="Create trail" onDismiss={onClose} footer={<><AwsButton onClick={onClose}>Cancel</AwsButton><AwsButton variant="primary" disabled={!name || !bucket || create.isPending} onClick={() => create.mutate()}>Create trail</AwsButton></>}>
      <SpaceBetween size="s">
        <FormField label="Trail name"><Input value={name} onChange={(event) => setName(event.detail.value)} /></FormField>
        <FormField label="Amazon S3 bucket name"><Input value={bucket} onChange={(event) => setBucket(event.detail.value)} /></FormField>
        <Checkbox checked={createBucket} onChange={(event) => setCreateBucket(event.detail.checked)}>
          Create a new Amazon S3 bucket
        </Checkbox>
        {create.isError && <AwsErrorAlert>{create.error instanceof Error ? create.error.message : "The request failed."}</AwsErrorAlert>}
      </SpaceBetween>
    </AwsModal>
  );
}

function EventDetailModal({ event, onClose }: { event: CloudTrailEvent; onClose: () => void }) {
  let formatted = event.cloudTrailEvent;
  try {
    formatted = JSON.stringify(JSON.parse(event.cloudTrailEvent), null, 2);
  } catch {
    // The API's raw event string is still the authoritative representation.
  }
  return (
    <AwsModal title={event.eventName} onDismiss={onClose} footer={<AwsButton onClick={onClose}>Close</AwsButton>}>
      <p><strong>Event source</strong><br /><code>{event.eventSource}</code></p>
      <p><strong>Event ID</strong><br /><code>{event.eventId}</code></p>
      <pre>{formatted}</pre>
    </AwsModal>
  );
}

export function CloudTrailPage() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [eventDetail, setEventDetail] = useState<CloudTrailEvent | null>(null);
  const logging = useMutation({
    mutationFn: async ({ trails, enabled }: { trails: CloudTrailTrail[]; enabled: boolean }) => {
      for (const trail of trails) await (enabled ? startCloudTrailLogging(trail.name) : stopCloudTrailLogging(trail.name));
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["cloudtrail-trails"] }),
  });
  const remove = useMutation({
    mutationFn: async (trails: CloudTrailTrail[]) => {
      for (const trail of trails) await deleteCloudTrailTrail(trail.name);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["cloudtrail-trails"] }),
  });
  return (
    <>
      <AwsResourceTable<CloudTrailTrail>
        title="Trails"
        description="AWS CloudTrail trails in this account and Region."
        columns={trailColumns}
        queryKey={["cloudtrail-trails"]}
        queryFn={fetchCloudTrailTrails}
        filterPlaceholder="Find trails"
        emptyTitle="No trails"
        emptyDescription="No AWS CloudTrail trails exist in this account and Region."
        rowKey={(row) => row.trailARN || row.name}
        tableTestId="cloudtrail-trails-table"
        errorTestId="cloudtrail-trails-error"
        actions={({ selected, clearSelection, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length === 0 || logging.isPending} onClick={() => logging.mutate({ trails: selected, enabled: true })}>Start logging</AwsButton>
            <AwsButton disabled={selected.length === 0 || logging.isPending} onClick={() => logging.mutate({ trails: selected, enabled: false })}>Stop logging</AwsButton>
            <AwsButton disabled={selected.length === 0 || remove.isPending} onClick={() => remove.mutate(selected, { onSuccess: clearSelection })}>Delete</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
            <AwsButton variant="primary" onClick={() => setCreating(true)}>Create trail</AwsButton>
          </>
        )}
      />
      <AwsResourceTable<CloudTrailEvent>
        title="Event history"
        headingVariant="h2"
        description="The management events CloudTrail recorded for this account."
        columns={eventColumns}
        queryKey={["cloudtrail-events"]}
        queryFn={fetchCloudTrailEvents}
        filterPlaceholder="Find events"
        emptyTitle="No events"
        emptyDescription="CloudTrail has recorded no management events for this account yet."
        rowKey={(row) => row.eventId}
        tableTestId="cloudtrail-events-table"
        errorTestId="cloudtrail-events-error"
        actions={({ selected, refetch, isFetching }) => (
          <>
            <AwsButton disabled={selected.length !== 1} onClick={() => setEventDetail(selected[0] ?? null)}>View event</AwsButton>
            <AwsButton onClick={refetch} disabled={isFetching}>{isFetching ? "Refreshing…" : "Refresh"}</AwsButton>
          </>
        )}
      />
      {creating && <CreateTrailModal onClose={() => setCreating(false)} />}
      {eventDetail && <EventDetailModal event={eventDetail} onClose={() => setEventDetail(null)} />}
    </>
  );
}
