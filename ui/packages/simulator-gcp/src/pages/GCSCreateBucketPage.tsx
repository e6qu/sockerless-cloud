import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import { gcsBucketRequest, sendRequest, type GCSBucket } from "../api.js";
import { STORAGE_CLASSES, storageClassLabel } from "./GCSBucketsPage.js";

// Cloud Storage's bucket-name contract: 3–63 characters of lowercase letters,
// digits, hyphens, underscores and dots, starting and ending with a letter or
// digit.
export const BUCKET_NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$/;

// The location names Cloud Storage documents for each location type.
const LOCATIONS = {
  "multi-region": [
    { id: "US", label: "us (multiple regions in United States)" },
    { id: "EU", label: "eu (multiple regions in European Union)" },
    { id: "ASIA", label: "asia (multiple regions in Asia)" },
  ],
  "dual-region": [
    { id: "NAM4", label: "nam4 (Iowa and South Carolina)" },
    { id: "EUR4", label: "eur4 (Netherlands and Finland)" },
    { id: "ASIA1", label: "asia1 (Tokyo and Osaka)" },
  ],
  region: [
    { id: "US-CENTRAL1", label: "us-central1 (Iowa)" },
    { id: "US-EAST1", label: "us-east1 (South Carolina)" },
    { id: "US-WEST1", label: "us-west1 (Oregon)" },
    { id: "EUROPE-WEST1", label: "europe-west1 (Belgium)" },
    { id: "EUROPE-WEST4", label: "europe-west4 (Netherlands)" },
    { id: "ASIA-EAST1", label: "asia-east1 (Taiwan)" },
    { id: "ASIA-NORTHEAST1", label: "asia-northeast1 (Tokyo)" },
  ],
} as const;

type LocationType = keyof typeof LOCATIONS;

const LOCATION_TYPE_LABELS: Record<LocationType, string> = {
  "multi-region": "Multi-region",
  "dual-region": "Dual-region",
  region: "Region",
};

const STORAGE_CLASS_HINTS: Record<string, string> = {
  STANDARD: "Best for short-term storage and frequently accessed data",
  NEARLINE: "Best for backups and data accessed less than once a month",
  COLDLINE: "Best for disaster recovery and data accessed less than once a quarter",
  ARCHIVE: "Best for long-term digital preservation of data accessed less than once a year",
};

export function GCSCreateBucketPage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [name, setName] = useState("");
  const [labels, setLabels] = useState<LabelPair[]>([]);
  const [locationType, setLocationType] = useState<LocationType>("multi-region");
  const [location, setLocation] = useState<string>(LOCATIONS["multi-region"][0].id);
  const [storageClass, setStorageClass] = useState<string>("STANDARD");
  const [publicAccessPrevention, setPublicAccessPrevention] = useState(true);
  const [uniformAccess, setUniformAccess] = useState(true);

  const nameError =
    name && !BUCKET_NAME_PATTERN.test(name)
      ? "Use 3 to 63 lowercase letters, numbers, hyphens, underscores or dots, starting and ending with a letter or number."
      : null;
  const request = gcsBucketRequest(project, {
    name,
    location,
    storageClass,
    uniformAccess,
    publicAccessPrevention,
    labels: pairsToLabels(labels),
  });

  const create = useMutation({
    mutationFn: () => track(`Create bucket "${name}"`, () => sendRequest<GCSBucket>(request), `Created bucket "${name}"`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["gcs-buckets-real", project] });
      navigate(path(`/ui/storage/browser/${name}`));
    },
  });

  return (
    <CreatePage
      title="Create a bucket"
      backTo="/ui/storage/browser"
      testId="gcs-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(name && !nameError)}
      error={create.error}
      errorTitle="Couldn't create the bucket."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Get started">
        <Field label="Name your bucket" error={nameError} hint="Bucket names are globally unique and can't be changed later.">
          <input type="text" value={name} data-testid="gcs-create-name" onChange={(event) => setName(event.target.value)} />
        </Field>
        <LabelsEditor pairs={labels} onChange={setLabels} idPrefix="gcs-create" />
      </FormSection>
      <FormSection title="Choose where to store your data" description="The location can't be changed after the bucket is created.">
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Location type</legend>
          {(Object.keys(LOCATIONS) as LocationType[]).map((type) => (
            <label className="gc-radio" key={type}>
              <input
                type="radio"
                name="location-type"
                checked={locationType === type}
                onChange={() => {
                  setLocationType(type);
                  setLocation(LOCATIONS[type][0].id);
                }}
              />
              {LOCATION_TYPE_LABELS[type]}
            </label>
          ))}
        </fieldset>
        <Field label="Location">
          <select value={location} data-testid="gcs-create-location" onChange={(event) => setLocation(event.target.value)}>
            {LOCATIONS[locationType].map((candidate) => (
              <option key={candidate.id} value={candidate.id}>
                {candidate.label}
              </option>
            ))}
          </select>
        </Field>
      </FormSection>
      <FormSection title="Choose how to store your data">
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Default storage class</legend>
          {STORAGE_CLASSES.map((candidate) => (
            <label className="gc-radio" key={candidate}>
              <input
                type="radio"
                name="storage-class"
                checked={storageClass === candidate}
                onChange={() => setStorageClass(candidate)}
              />
              <span>
                {storageClassLabel(candidate)}
                <span className="gc-radio-note">{STORAGE_CLASS_HINTS[candidate]}</span>
              </span>
            </label>
          ))}
        </fieldset>
      </FormSection>
      <FormSection title="Choose how to control access to objects">
        <label className="gc-checkbox">
          <input
            type="checkbox"
            checked={publicAccessPrevention}
            onChange={(event) => setPublicAccessPrevention(event.target.checked)}
          />
          Enforce public access prevention on this bucket
        </label>
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Access control</legend>
          <label className="gc-radio">
            <input type="radio" name="access-control" checked={uniformAccess} onChange={() => setUniformAccess(true)} />
            <span>
              Uniform
              <span className="gc-radio-note">Ensure uniform access to all objects in the bucket by using only bucket-level permissions (IAM).</span>
            </span>
          </label>
          <label className="gc-radio">
            <input type="radio" name="access-control" checked={!uniformAccess} onChange={() => setUniformAccess(false)} />
            <span>
              Fine-grained
              <span className="gc-radio-note">Specify access to individual objects by using object-level permissions (ACLs) in addition to bucket-level permissions.</span>
            </span>
          </label>
        </fieldset>
      </FormSection>
    </CreatePage>
  );
}
