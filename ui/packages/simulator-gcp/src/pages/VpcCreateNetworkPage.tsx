import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CreatePage, Field, FormSection } from "../console/forms.js";
import { useOperations } from "../console/notifications.js";
import { useProject, useProjectPath } from "../console/project.js";
import { shortName } from "../console/format.js";
import {
  fetchComputeZones,
  sendRequest,
  subnetworkRequest,
  vpcNetworkRequest,
  waitComputeOperation,
  type ComputeOperation,
} from "../api.js";
import { RESOURCE_NAME_PATTERN, regionOf } from "./ComputeCreateInstancePage.js";

const CIDR_PATTERN = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;

// Create a VPC network: networks.insert, then for a custom-mode network the
// one subnet the form describes through subnetworks.insert, each driven to DONE.
export function VpcCreateNetworkPage() {
  const { project } = useProject();
  const navigate = useNavigate();
  const path = useProjectPath();
  const queryClient = useQueryClient();
  const { track } = useOperations();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [mode, setMode] = useState<"auto" | "custom">("auto");
  const [subnetName, setSubnetName] = useState("");
  const [subnetRegion, setSubnetRegion] = useState("us-central1");
  const [subnetRange, setSubnetRange] = useState("10.0.0.0/24");
  const [routingMode, setRoutingMode] = useState<"REGIONAL" | "GLOBAL">("REGIONAL");
  const [mtu, setMtu] = useState(1460);
  const zones = useQuery({ queryKey: ["compute-zones", project], queryFn: () => fetchComputeZones(project) });
  const regions = Array.from(
    new Set((zones.data ?? []).map((zone) => shortName(zone.region ?? regionOf(zone.name)))),
  ).sort();

  const request = vpcNetworkRequest(project, {
    name,
    description,
    autoCreateSubnetworks: mode === "auto",
    routingMode,
    mtu,
  });
  const nameError = name && !RESOURCE_NAME_PATTERN.test(name) ? "Use lowercase letters, numbers and hyphens, starting with a letter." : null;
  const subnetValid = mode === "auto" || (RESOURCE_NAME_PATTERN.test(subnetName) && CIDR_PATTERN.test(subnetRange));

  const create = useMutation({
    mutationFn: () =>
      track(
        `Create VPC network "${name}"`,
        async () => {
          await waitComputeOperation(project, "global", await sendRequest<ComputeOperation>(request));
          if (mode === "custom") {
            const subnet = subnetworkRequest(project, name, { name: subnetName, region: subnetRegion, ipCidrRange: subnetRange });
            await waitComputeOperation(project, `regions/${subnetRegion}`, await sendRequest<ComputeOperation>(subnet));
          }
        },
        `Created VPC network "${name}"`,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["compute-networks", project] });
      navigate(path(`/ui/networking/networks/details/${name}`));
    },
  });

  return (
    <CreatePage
      title="Create a VPC network"
      backTo="/ui/networking/networks/list"
      testId="vpc-create-form"
      submitting={create.isPending}
      canSubmit={Boolean(name && !nameError && subnetValid)}
      error={create.error}
      errorTitle="Couldn't create the VPC network."
      onSubmit={() => create.mutate()}
      request={request}
    >
      <FormSection title="Network details">
        <Field label="Name" error={nameError}>
          <input type="text" value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <Field label="Description">
          <textarea value={description} rows={2} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <Field label="Maximum transmission unit (MTU)">
          <select value={mtu} onChange={(event) => setMtu(Number(event.target.value))}>
            <option value={1460}>1460</option>
            <option value={1500}>1500</option>
            <option value={8896}>8896</option>
          </select>
        </Field>
      </FormSection>
      <FormSection title="Subnets" description="Subnets let you create your own private cloud topology within Google Cloud.">
        <fieldset className="gc-radio-group">
          <legend className="gc-field-label">Subnet creation mode</legend>
          <label className="gc-radio">
            <input type="radio" name="subnet-mode" checked={mode === "custom"} onChange={() => setMode("custom")} />
            Custom
          </label>
          <label className="gc-radio">
            <input type="radio" name="subnet-mode" checked={mode === "auto"} onChange={() => setMode("auto")} />
            Automatic
          </label>
        </fieldset>
        {mode === "custom" ? (
          <div className="gc-subform">
            <h3 className="gc-subform-title">New subnet</h3>
            <Field label="Name">
              <input type="text" value={subnetName} onChange={(event) => setSubnetName(event.target.value)} />
            </Field>
            <Field label="Region">
              <select value={subnetRegion} onChange={(event) => setSubnetRegion(event.target.value)}>
                {regions.map((region) => (
                  <option key={region} value={region}>
                    {region}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="IPv4 range" hint="For example, 10.0.0.0/24">
              <input type="text" value={subnetRange} onChange={(event) => setSubnetRange(event.target.value)} />
            </Field>
          </div>
        ) : null}
      </FormSection>
      <FormSection title="Dynamic routing mode">
        <fieldset className="gc-radio-group">
          <legend className="gc-visually-hidden">Dynamic routing mode</legend>
          <label className="gc-radio">
            <input type="radio" name="routing" checked={routingMode === "REGIONAL"} onChange={() => setRoutingMode("REGIONAL")} />
            Regional
          </label>
          <label className="gc-radio">
            <input type="radio" name="routing" checked={routingMode === "GLOBAL"} onChange={() => setRoutingMode("GLOBAL")} />
            Global
          </label>
        </fieldset>
      </FormSection>
    </CreatePage>
  );
}
