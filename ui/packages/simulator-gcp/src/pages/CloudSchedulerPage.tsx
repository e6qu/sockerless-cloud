import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GcpResourceTable, GcpStatus, type GcpColumn } from "../console/index.js";
import { GcpDialog } from "../console/GcpDialog.js";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { formatTimestamp, shortName } from "../console/format.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import {
  CONSOLE_REGION,
  deleteSchedulerJob,
  fetchPubSubTopics,
  fetchSchedulerJobs,
  schedulerJobAction,
  schedulerJobRequest,
  sendRequest,
  type SchedulerJob,
} from "../api.js";

const regionOf = (name: string) => name.split("/")[3] ?? CONSOLE_REGION;

const stateLabel = (job: SchedulerJob) =>
  job.state ? job.state.charAt(0) + job.state.slice(1).toLowerCase().replace(/_/g, " ") : "Unknown";

// The result of the last attempt: the job's status carries a google.rpc
// code, 0 when the attempt succeeded, and is absent before the first one.
export function lastRunResult(job: SchedulerJob): string {
  if (!job.lastAttemptTime) return "Has not run yet";
  return job.status?.code ? "Failed" : "Success";
}

const target = (job: SchedulerJob) =>
  job.httpTarget ? `URL : ${job.httpTarget.uri ?? ""}` : job.pubsubTarget ? `Topic : ${shortName(job.pubsubTarget.topicName ?? "")}` : "—";

const columns: GcpColumn<SchedulerJob>[] = [
  { id: "name", header: "Name", cell: (row) => shortName(row.name), value: (row) => shortName(row.name) },
  { id: "region", header: "Region", cell: (row) => regionOf(row.name), value: (row) => regionOf(row.name) },
  {
    id: "state",
    header: "State",
    cell: (row) => <GcpStatus status={row.state ?? "UNKNOWN"} kind={row.state === "PAUSED" ? "inactive" : undefined} label={stateLabel(row)} />,
    value: stateLabel,
  },
  { id: "description", header: "Description", cell: (row) => row.description || "—", value: (row) => row.description ?? "" },
  {
    id: "frequency",
    header: "Frequency",
    cell: (row) => `${row.schedule ?? ""} (${row.timeZone ?? "Etc/UTC"})`,
    value: (row) => row.schedule ?? "",
  },
  { id: "target", header: "Target", cell: target, value: target },
  { id: "lastRun", header: "Last run", cell: (row) => formatTimestamp(row.lastAttemptTime ?? ""), value: (row) => row.lastAttemptTime ?? "" },
  {
    id: "lastResult",
    header: "Last run result",
    cell: (row) => {
      const result = lastRunResult(row);
      return result === "Has not run yet" ? result : <GcpStatus status={result} kind={result === "Success" ? "success" : "error"} />;
    },
    value: lastRunResult,
  },
  { id: "nextRun", header: "Next run", cell: (row) => formatTimestamp(row.scheduleTime ?? ""), value: (row) => row.scheduleTime ?? "" },
];

function DeleteJobsDialog({ jobs, onClose, onDeleted }: { jobs: SchedulerJob[]; onClose: () => void; onDeleted: () => void }) {
  const { track } = useOperations();
  const subject = jobs.length === 1 ? `job "${shortName(jobs[0].name)}"` : `${jobs.length} jobs`;
  const remove = useMutation({
    mutationFn: () => track(`Delete ${subject}`, () => Promise.all(jobs.map((job) => deleteSchedulerJob(job.name))), `Deleted ${subject}`),
    onSuccess: onDeleted,
  });
  return (
    <GcpDialog title={`Delete ${subject}?`} testId="scheduler-delete-dialog" onClose={onClose}>
      <p>Deleted jobs stop running and can't be recovered.</p>
      {remove.isError ? (
        <div className="gc-message gc-message-error" role="alert">
          <strong>Couldn't delete {subject}.</strong> {remove.error instanceof Error ? remove.error.message : String(remove.error)}
        </div>
      ) : null}
      <div className="gc-dialog-actions">
        <button type="button" className="gc-button-text" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="gc-button-primary gc-button-danger" data-testid="scheduler-delete-confirm" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? "Deleting…" : "Delete"}
        </button>
      </div>
    </GcpDialog>
  );
}

export function CloudSchedulerPage() {
  const { project } = useProject();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [deleting, setDeleting] = useState<SchedulerJob[] | null>(null);
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["scheduler-jobs", project] });
  const act = (jobs: SchedulerJob[], action: "run" | "pause" | "resume", verb: string) => {
    const subject = jobs.length === 1 ? `job "${shortName(jobs[0].name)}"` : `${jobs.length} jobs`;
    void track(`${verb} ${subject}`, () => Promise.all(jobs.map((job) => schedulerJobAction(job.name, action)))).then(refresh, refresh);
  };
  return (
    <>
      <GcpResourceTable<SchedulerJob>
        title="Scheduler jobs"
        actions={[{ label: "Create job", icon: "add", to: "/ui/cloudscheduler/jobs/new", testId: "scheduler-create" }]}
        selectionActions={(selected) => [
          { label: "Force run", icon: "play_arrow", onSelect: () => act(selected, "run", "Force run") },
          { label: "Pause", icon: "pause_circle", onSelect: () => act(selected, "pause", "Pause") },
          { label: "Resume", icon: "sync", onSelect: () => act(selected, "resume", "Resume") },
          { label: "Delete", icon: "delete", onSelect: () => setDeleting(selected) },
        ]}
        rowActions={(row) => [
          { label: "Force run", onSelect: () => act([row], "run", "Force run") },
          row.state === "PAUSED"
            ? { label: "Resume", onSelect: () => act([row], "resume", "Resume") }
            : { label: "Pause", onSelect: () => act([row], "pause", "Pause") },
          { label: "Delete", onSelect: () => setDeleting([row]) },
        ]}
        columns={columns}
        queryKey={["scheduler-jobs", project]}
        queryFn={() => fetchSchedulerJobs(project)}
        resourceNoun="jobs"
        testId="scheduler-jobs-table"
        empty={{ headline: "No jobs", description: "Cloud Scheduler runs jobs on a cron schedule against an HTTP target or a Pub/Sub topic." }}
        rowKey={(row) => row.name}
        rowLabel={(row) => shortName(row.name)}
      />
      {deleting ? (
        <DeleteJobsDialog
          jobs={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            refresh();
          }}
        />
      ) : null}
    </>
  );
}

const JOB_ID_PATTERN = /^[A-Za-z0-9_-]{1,500}$/;
const HTTP_METHODS = ["POST", "GET", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

export function CloudSchedulerCreatePage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const topics = useQuery({ queryKey: ["pubsub-topics", project], queryFn: () => fetchPubSubTopics(project) });
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [schedule, setSchedule] = useState("*/5 * * * *");
  const [timeZone, setTimeZone] = useState("Etc/UTC");
  const [targetType, setTargetType] = useState<"http" | "pubsub">("pubsub");
  const [uri, setUri] = useState("");
  const [httpMethod, setHttpMethod] = useState("POST");
  const [topic, setTopic] = useState("");
  const [body, setBody] = useState("");
  const request = schedulerJobRequest(project, { name, description, schedule, timeZone, target: targetType, uri, httpMethod, topic, body });
  const nameError = name && !JOB_ID_PATTERN.test(name) ? "Use letters, numbers, hyphens and underscores only." : null;
  const scheduleValid = schedule.trim().split(/\s+/).length === 5;
  const targetValid = targetType === "http" ? /^https?:\/\//.test(uri) : Boolean(topic && body);
  const create = useMutation({
    mutationFn: () => track(`Create job "${name}"`, () => sendRequest(request), `Created job "${name}"`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["scheduler-jobs", project] });
      navigate(path("/ui/cloudscheduler"));
    },
  });
  return (
    <CreatePage
      title="Create a job"
      backTo="/ui/cloudscheduler"
      testId="scheduler-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(name && !nameError && scheduleValid && targetValid)}
      error={create.error}
      errorTitle="Couldn't create the job."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Define the schedule">
        <Field label="Name" error={nameError}>
          <input type="text" value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <Field label="Region">
          <select value={CONSOLE_REGION} disabled>
            <option value={CONSOLE_REGION}>{CONSOLE_REGION}</option>
          </select>
        </Field>
        <Field label="Description">
          <input type="text" value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <Field label="Frequency" hint="A unix-cron schedule, for example */5 * * * * runs every five minutes." error={scheduleValid ? null : "Enter five cron fields."}>
          <input type="text" value={schedule} onChange={(event) => setSchedule(event.target.value)} />
        </Field>
        <Field label="Time zone" hint="An IANA time zone, for example America/New_York.">
          <input type="text" value={timeZone} onChange={(event) => setTimeZone(event.target.value)} />
        </Field>
      </FormSection>
      <FormSection title="Configure the execution">
        <Field label="Target type">
          <select value={targetType} onChange={(event) => setTargetType(event.target.value as "http" | "pubsub")}>
            <option value="pubsub">Pub/Sub</option>
            <option value="http">HTTP</option>
          </select>
        </Field>
        {targetType === "http" ? (
          <>
            <Field label="URL">
              <input type="url" value={uri} onChange={(event) => setUri(event.target.value)} />
            </Field>
            <Field label="HTTP method">
              <select value={httpMethod} onChange={(event) => setHttpMethod(event.target.value)}>
                {HTTP_METHODS.map((method) => (
                  <option key={method} value={method}>
                    {method}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Body">
              <textarea rows={3} value={body} onChange={(event) => setBody(event.target.value)} />
            </Field>
          </>
        ) : (
          <>
            <Field label="Select a Cloud Pub/Sub topic">
              <select value={topic} onChange={(event) => setTopic(event.target.value)}>
                <option value="">Select a topic</option>
                {(topics.data ?? []).map((candidate) => (
                  <option key={candidate.name} value={shortName(candidate.name)}>
                    {candidate.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Message body">
              <textarea rows={3} value={body} onChange={(event) => setBody(event.target.value)} />
            </Field>
          </>
        )}
      </FormSection>
    </CreatePage>
  );
}
