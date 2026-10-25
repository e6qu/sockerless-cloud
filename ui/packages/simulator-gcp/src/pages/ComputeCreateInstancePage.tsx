import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { LabelsEditor, pairsToLabels, type LabelPair } from "../console/LabelsEditor.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import { shortName } from "../console/format.js";
import {
  computeInstanceRequest,
  fetchComputeNetworks,
  fetchComputeZones,
  fetchMachineTypes,
  fetchPublicImages,
  sendRequest,
  waitComputeZoneOperation,
  type ComputeOperation,
} from "../api.js";
import { instancePath } from "./ComputeEnginePage.js";

// The public image projects the boot disk picker offers, by the operating
// system name the real console shows.
const OPERATING_SYSTEMS = [
  { project: "debian-cloud", name: "Debian GNU/Linux" },
  { project: "ubuntu-os-cloud", name: "Ubuntu" },
];

const DISK_TYPES = [
  { id: "pd-balanced", name: "Balanced persistent disk" },
  { id: "pd-standard", name: "Standard persistent disk" },
  { id: "pd-ssd", name: "SSD persistent disk" },
];

// Compute Engine resource names: lowercase letters, digits and hyphens,
// starting with a letter, at most 63 characters (RFC 1035).
export const RESOURCE_NAME_PATTERN = /^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$/;

export function regionOf(zone: string): string {
  return zone.replace(/-[a-z]$/, "");
}

export function ComputeCreateInstancePage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();

  const [name, setName] = useState("instance-1");
  const [labels, setLabels] = useState<LabelPair[]>([]);
  const [region, setRegion] = useState("");
  const [zone, setZone] = useState("");
  const [machineType, setMachineType] = useState("e2-medium");
  const [osProject, setOsProject] = useState(OPERATING_SYSTEMS[0].project);
  const [image, setImage] = useState("");
  const [diskType, setDiskType] = useState(DISK_TYPES[0].id);
  const [diskSize, setDiskSize] = useState(10);
  const [network, setNetwork] = useState("");
  const [externalIp, setExternalIp] = useState(true);

  const zones = useQuery({ queryKey: ["compute-zones", project], queryFn: () => fetchComputeZones(project) });
  const machineTypes = useQuery({
    queryKey: ["compute-machine-types", project, zone],
    queryFn: () => fetchMachineTypes(project, zone),
    enabled: zone !== "",
  });
  const images = useQuery({ queryKey: ["compute-images", osProject], queryFn: () => fetchPublicImages(osProject) });
  const networks = useQuery({ queryKey: ["compute-networks", project], queryFn: () => fetchComputeNetworks(project) });

  const regions = useMemo(
    () => Array.from(new Set((zones.data ?? []).map((candidate) => shortName(candidate.region ?? regionOf(candidate.name))))).sort(),
    [zones.data],
  );
  const regionZones = (zones.data ?? []).filter((candidate) => regionOf(candidate.name) === region);

  useEffect(() => {
    if (!region && regions.length > 0) setRegion(regions.includes("us-central1") ? "us-central1" : regions[0]);
  }, [region, regions]);
  useEffect(() => {
    if (regionZones.length > 0 && !regionZones.some((candidate) => candidate.name === zone)) setZone(regionZones[0].name);
  }, [regionZones, zone]);
  // A boot image must match the machine type's CPU architecture; machine
  // types that name none are x86.
  const architecture = machineTypes.data?.find((candidate) => candidate.name === machineType)?.architecture ?? "X86_64";
  const available = useMemo(
    () =>
      (images.data ?? []).filter(
        (candidate) => !candidate.deprecated && (candidate.architecture ?? "X86_64") === architecture,
      ),
    [images.data, architecture],
  );
  useEffect(() => {
    if (available.length > 0 && !available.some((candidate) => candidate.name === image)) setImage(available[0].name);
  }, [available, image]);
  useEffect(() => {
    const names = (networks.data ?? []).map((candidate) => candidate.name);
    if (names.length > 0 && !names.includes(network)) setNetwork(names.includes("default") ? "default" : names[0]);
  }, [networks.data, network]);

  const nameError = name && !RESOURCE_NAME_PATTERN.test(name)
    ? "Name must start with a lowercase letter followed by up to 62 lowercase letters, numbers, or hyphens, and can't end with a hyphen."
    : null;
  const request = computeInstanceRequest(project, {
    name,
    zone,
    machineType,
    sourceImage: `projects/${osProject}/global/images/${image}`,
    diskSizeGb: diskSize,
    diskType,
    network,
    labels: pairsToLabels(labels),
    externalIp,
  });

  const create = useMutation({
    mutationFn: () =>
      track(
        `Create VM instance "${name}"`,
        async () => waitComputeZoneOperation(project, zone, await sendRequest<ComputeOperation>(request)),
        `Created VM instance "${name}"`,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["compute-instances", project] });
      navigate(path(instancePath(zone, name)));
    },
  });

  return (
    <CreatePage
      title="Create an instance"
      backTo="/ui/compute/instances"
      testId="compute-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(name && !nameError && zone && image && network && diskSize >= 10)}
      error={create.error}
      errorTitle="Couldn't create the instance."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Machine configuration">
        <Field label="Name" error={nameError} hint="Permanent">
          <input type="text" value={name} required onChange={(event) => setName(event.target.value)} />
        </Field>
        <LabelsEditor pairs={labels} onChange={setLabels} idPrefix="compute-create" />
        <div className="gc-field-row">
          <Field label="Region" hint="Permanent">
            <select value={region} onChange={(event) => setRegion(event.target.value)} disabled={zones.isLoading}>
              {regions.map((candidate) => (
                <option key={candidate} value={candidate}>
                  {candidate}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Zone" hint="Permanent">
            <select value={zone} onChange={(event) => setZone(event.target.value)}>
              {regionZones.map((candidate) => (
                <option key={candidate.name} value={candidate.name}>
                  {candidate.name}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field
          label="Machine type"
          hint={(() => {
            const selected = machineTypes.data?.find((candidate) => candidate.name === machineType);
            return selected ? `${selected.guestCpus ?? "?"} vCPU, ${((selected.memoryMb ?? 0) / 1024).toFixed(1)} GB memory` : undefined;
          })()}
        >
          <select value={machineType} onChange={(event) => setMachineType(event.target.value)}>
            {(machineTypes.data ?? [{ name: machineType }]).map((candidate) => (
              <option key={candidate.name} value={candidate.name}>
                {candidate.name}
              </option>
            ))}
          </select>
        </Field>
      </FormSection>
      <FormSection title="OS and storage" description="The boot disk the instance starts from.">
        <div className="gc-field-row">
          <Field label="Operating system">
            <select value={osProject} onChange={(event) => setOsProject(event.target.value)}>
              {OPERATING_SYSTEMS.map((candidate) => (
                <option key={candidate.project} value={candidate.project}>
                  {candidate.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Version">
            <select value={image} onChange={(event) => setImage(event.target.value)}>
              {available.map((candidate) => (
                  <option key={candidate.name} value={candidate.name}>
                    {candidate.name}
                  </option>
                ))}
            </select>
          </Field>
        </div>
        <div className="gc-field-row">
          <Field label="Boot disk type">
            <select value={diskType} onChange={(event) => setDiskType(event.target.value)}>
              {DISK_TYPES.map((candidate) => (
                <option key={candidate.id} value={candidate.id}>
                  {candidate.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Size (GB)" hint="At least 10 GB">
            <input type="number" min={10} value={diskSize} onChange={(event) => setDiskSize(Number(event.target.value))} />
          </Field>
        </div>
      </FormSection>
      <FormSection title="Networking">
        <Field
          label="Network"
          error={networks.data && networks.data.length === 0 ? "This project has no VPC network. Create one in VPC network first." : null}
        >
          <select value={network} onChange={(event) => setNetwork(event.target.value)}>
            {(networks.data ?? []).map((candidate) => (
              <option key={candidate.name} value={candidate.name}>
                {candidate.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="External IPv4 address">
          <select value={externalIp ? "ephemeral" : "none"} onChange={(event) => setExternalIp(event.target.value === "ephemeral")}>
            <option value="ephemeral">Ephemeral</option>
            <option value="none">None</option>
          </select>
        </Field>
      </FormSection>
    </CreatePage>
  );
}
