import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  Dropdown,
  Field,
  Input,
  Link,
  Option,
  Radio,
  RadioGroup,
  Spinner,
  Tab,
  TabList,
  Text,
} from "@fluentui/react-components";
import { armFetch } from "../../portal/arm.js";
import { listLocations, listResourceGroups, listSubscriptions, putResource } from "../../azure.js";
import { API_VERSION } from "../../api.js";
import { serviceById } from "../../catalog.js";
import { RESOURCE_GROUP_TYPE } from "../../services.js";
import { Blade, EmptyState, ErrorBar, InfoBar, errorText } from "../../portal/AzurePortal.js";
import { deploymentName, startDeployment, type DeploymentStep } from "../../portal/deployments.js";
import { useNotifications } from "../../portal/notifications.js";
import { DEPLOYMENT_ROUTE, resourceRoute } from "../../portal/routes.js";
import { specForRoute, type CreateField, type CreateSpec, type CreateTab } from "./specs.js";

type WizardTab = CreateTab | "Tags" | "Review + create";

interface TagRow {
  name: string;
  value: string;
}

function FieldControl({ field, value, onChange, error }: { field: CreateField; value: string; onChange: (value: string) => void; error?: string }) {
  if (field.checkbox) {
    return <Checkbox label={field.label} checked={value === "true"} onChange={(_, data) => onChange(data.checked ? "true" : "false")} />;
  }
  return (
    <Field label={field.label} required={field.required} hint={field.info} validationMessage={error} validationState={error ? "error" : "none"} orientation="horizontal" className="az-form-field">
      {field.radio && field.options ? (
        <RadioGroup value={value} onChange={(_, data) => onChange(data.value)}>
          {field.options.map((option) => (
            <Radio key={option.value} value={option.value} label={option.label} />
          ))}
        </RadioGroup>
      ) : field.options ? (
        <Dropdown
          value={field.options.find((option) => option.value === value)?.label ?? value}
          selectedOptions={[value]}
          onOptionSelect={(_, data) => onChange(data.optionValue ?? "")}
        >
          {field.options.map((option) => (
            <Option key={option.value} value={option.value}>
              {option.label}
            </Option>
          ))}
        </Dropdown>
      ) : (
        <Input value={value} onChange={(_, data) => onChange(data.value)} />
      )}
    </Field>
  );
}

function Wizard({ spec }: { spec: CreateSpec }) {
  const navigate = useNavigate();
  const { start, settle } = useNotifications();
  const service = serviceById(spec.serviceId);
  const isGroup = spec.type === RESOURCE_GROUP_TYPE;
  const tabs: WizardTab[] = [
    "Basics",
    ...(["Networking", "Advanced"] as const).filter((tab) => spec.fields.some((field) => field.tab === tab)),
    "Tags",
    "Review + create",
  ];
  const [tab, setTab] = useState<WizardTab>("Basics");
  const [subscriptionId, setSubscriptionId] = useState("");
  const [resourceGroup, setResourceGroup] = useState("");
  const [newGroup, setNewGroup] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [location, setLocation] = useState("eastus");
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(spec.fields.map((field) => [field.id, field.defaultValue])));
  const [tags, setTags] = useState<TagRow[]>([{ name: "", value: "" }]);
  const [showErrors, setShowErrors] = useState(false);
  const [nameAvailability, setNameAvailability] = useState<{ name: string; available: boolean; message: string } | null>(null);
  const [checking, setChecking] = useState(false);
  const [submitError, setSubmitError] = useState<string>();

  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions });
  useEffect(() => {
    if (!subscriptionId && subscriptions.data?.[0]) setSubscriptionId(subscriptions.data[0].subscriptionId);
  }, [subscriptions.data, subscriptionId]);
  const groups = useQuery({ queryKey: ["resource-groups", subscriptionId], queryFn: () => listResourceGroups(subscriptionId), enabled: Boolean(subscriptionId) });
  const locations = useQuery({ queryKey: ["locations", subscriptionId], queryFn: () => listLocations(subscriptionId), enabled: Boolean(subscriptionId) });
  const subscription = subscriptions.data?.find((sub) => sub.subscriptionId === subscriptionId);
  const groupName = isGroup ? name : (newGroup ?? resourceGroup);

  const errors = useMemo(() => {
    const found: Record<string, string> = {};
    if (!subscriptionId) found.subscription = "Select a subscription.";
    if (!isGroup && !groupName) found.resourceGroup = "Select or create a resource group.";
    if (!isGroup && newGroup !== null && newGroup && !/^[-\w._()]{0,89}[-\w_()]$/u.test(newGroup)) found.resourceGroup = "Resource group names can contain letters, digits, underscores, parentheses, hyphens and periods.";
    if (!name) found.name = `${spec.nameLabel} is required.`;
    else if (!spec.namePattern.test(name)) found.name = spec.nameRule;
    else if (isGroup && groups.data?.some((group) => group.name.toLowerCase() === name.toLowerCase())) found.name = `A resource group named ${name} already exists in this subscription.`;
    else if (nameAvailability && nameAvailability.name === name && !nameAvailability.available) found.name = nameAvailability.message;
    if (!location) found.location = "Select a region.";
    for (const field of spec.fields) {
      const value = values[field.id] ?? "";
      if (field.required && !value) found[field.id] = `${field.label} is required.`;
      else if (field.pattern && !field.pattern.test(value)) found[field.id] = field.rule ?? "The value is not valid.";
    }
    for (const [index, row] of tags.entries()) {
      if (!row.name && row.value) found[`tag-${index}`] = "A tag needs a name.";
    }
    return found;
  }, [subscriptionId, isGroup, groupName, newGroup, name, spec, groups.data, nameAvailability, location, values, tags]);

  const tabOfError = (key: string): WizardTab => {
    if (key.startsWith("tag-")) return "Tags";
    const field = spec.fields.find((entry) => entry.id === key);
    return field ? field.tab : "Basics";
  };
  const errorTabs = new Set(Object.keys(errors).map(tabOfError));
  const shown = (key: string) => (showErrors || (key === "name" && name) ? errors[key] : undefined);

  useEffect(() => {
    if (!spec.checkName || !subscriptionId || !spec.namePattern.test(name)) return;
    const timer = setTimeout(async () => {
      setChecking(true);
      try {
        const response = await armFetch(
          `/subscriptions/${subscriptionId}/providers/${spec.checkName!.provider}/checkNameAvailability?api-version=${spec.checkName!.apiVersion}`,
          { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, type: spec.checkName!.type }) },
        );
        const result = (await response.json()) as { nameAvailable?: boolean; message?: string };
        setNameAvailability({ name, available: result.nameAvailable !== false, message: result.message || `The name ${name} is already in use.` });
      } finally {
        setChecking(false);
      }
    }, 400);
    return () => clearTimeout(timer);
  }, [spec, subscriptionId, name]);

  const create = () => {
    setShowErrors(true);
    if (Object.keys(errors).length > 0 || !subscription) return;
    const tagMap = Object.fromEntries(tags.filter((row) => row.name).map((row) => [row.name, row.value]));
    const groupId = `/subscriptions/${subscriptionId}/resourceGroups/${groupName}`;
    const resourceId = isGroup ? groupId : `${groupId}/providers/${spec.type}/${name}`;
    const body = spec.body({ subscriptionId, tenantId: subscription.tenantId, resourceGroup: groupName, location, name, tags: tagMap, values });
    const steps: DeploymentStep[] = [];
    if (!isGroup && newGroup !== null) {
      steps.push({
        resourceName: groupName,
        resourceType: RESOURCE_GROUP_TYPE,
        resourceId: groupId,
        run: (onProgress) => putResource(groupId, API_VERSION.resourceGroups, { location }, onProgress),
      });
    }
    steps.push({ resourceName: name, resourceType: spec.type, resourceId, run: (onProgress) => putResource(resourceId, spec.apiVersion, body, onProgress) });
    const deployment = deploymentName(spec.deploymentPrefix);
    const link = `${DEPLOYMENT_ROUTE}/${deployment}`;
    const notification = start("Deployment in progress...", `Deployment to resource group '${groupName}' is in progress.`, link);
    startDeployment(deployment, subscriptionId, groupName, resourceId, steps).then(
      () => settle(notification, "succeeded", "Deployment succeeded", `Deployment '${deployment}' to resource group '${groupName}' was successful.`, resourceRoute(resourceId)),
      (error: unknown) => settle(notification, "failed", "Deployment failed", errorText(error), link),
    );
    setSubmitError(undefined);
    navigate(link);
  };

  const index = tabs.indexOf(tab);
  const projectSection = (
    <section className="az-form-section" aria-labelledby="project-details">
      <Text as="h2" id="project-details" size={400} weight="semibold" block>
        Project details
      </Text>
      <Text block size={200} className="az-form-help">
        Select the subscription to manage deployed resources and costs. Use resource groups like folders to organize and manage all your resources.
      </Text>
      <Field label="Subscription" required orientation="horizontal" className="az-form-field" validationMessage={shown("subscription")}>
        <Dropdown
          value={subscription?.displayName ?? ""}
          selectedOptions={[subscriptionId]}
          onOptionSelect={(_, data) => {
            setSubscriptionId(data.optionValue ?? "");
            setResourceGroup("");
          }}
        >
          {(subscriptions.data ?? []).map((sub) => (
            <Option key={sub.subscriptionId} value={sub.subscriptionId}>
              {sub.displayName}
            </Option>
          ))}
        </Dropdown>
      </Field>
      {isGroup ? null : (
        <Field label="Resource group" required orientation="horizontal" className="az-form-field az-form-indent" validationMessage={shown("resourceGroup")} validationState={shown("resourceGroup") ? "error" : "none"}>
          <div className="az-rg-picker">
            {newGroup === null ? (
              <Dropdown
                placeholder="Select a resource group"
                value={resourceGroup}
                selectedOptions={resourceGroup ? [resourceGroup] : []}
                onOptionSelect={(_, data) => setResourceGroup(data.optionValue ?? "")}
                aria-label="Resource group"
              >
                {(groups.data ?? []).map((group) => (
                  <Option key={group.id} value={group.name}>
                    {group.name}
                  </Option>
                ))}
              </Dropdown>
            ) : (
              <Input value={newGroup} onChange={(_, data) => setNewGroup(data.value)} aria-label="New resource group name" placeholder="(New) resource group name" />
            )}
            <Link as="button" onClick={() => setNewGroup(newGroup === null ? "" : null)}>
              {newGroup === null ? "Create new" : "Use existing"}
            </Link>
          </div>
        </Field>
      )}
    </section>
  );

  const controls = (tabName: CreateTab, section: string) =>
    spec.fields
      .filter((field) => field.tab === tabName && field.section === section)
      .map((field) => (
        <FieldControl key={field.id} field={field} value={values[field.id] ?? ""} error={shown(field.id)} onChange={(value) => setValues({ ...values, [field.id]: value })} />
      ));
  // Basics' instance details join the name and region in one section.
  const fieldsFor = (tabName: CreateTab) => {
    const sections = Array.from(new Set(spec.fields.filter((field) => field.tab === tabName).map((field) => field.section))).filter(
      (section) => !(tabName === "Basics" && section === "Instance details"),
    );
    return sections.map((section) => (
      <section className="az-form-section" key={section} aria-label={section}>
        <Text as="h2" size={400} weight="semibold" block>
          {section}
        </Text>
        {controls(tabName, section)}
      </section>
    ));
  };

  const summary: [string, string][] = [
    ["Subscription", subscription?.displayName ?? ""],
    ...(isGroup ? [] : ([["Resource group", newGroup !== null ? `(New) ${newGroup}` : resourceGroup]] as [string, string][])),
    ["Region", locations.data?.find((entry) => entry.name === location)?.displayName ?? location],
    [spec.nameLabel, name],
    ...spec.fields.map((field) => [field.label, field.options?.find((option) => option.value === values[field.id])?.label ?? (field.checkbox ? (values[field.id] === "true" ? "Enabled" : "Disabled") : values[field.id])] as [string, string]),
    ["Tags", tags.filter((row) => row.name).map((row) => `${row.name} : ${row.value}`).join(", ") || "None"],
  ];

  return (
    <Blade
      title={spec.title}
      icon={{ name: service.icon, color: service.color }}
      trail={[{ label: "Home", to: "/ui/home" }, { label: service.label, to: service.route }, { label: spec.title }]}
      testid={`create-${spec.id}`}
    >
      <div className="az-wizard">
        <TabList selectedValue={tab} onTabSelect={(_, data) => setTab(data.value as WizardTab)} aria-label="Create steps">
          {tabs.map((entry) => (
            <Tab key={entry} value={entry}>
              {entry}
              {showErrors && errorTabs.has(entry) ? <span className="az-tab-error" aria-label=" (has errors)"> ●</span> : null}
            </Tab>
          ))}
        </TabList>
        <div className="az-wizard-body">
          {tab === "Basics" ? (
            <>
              {projectSection}
              <section className="az-form-section" aria-labelledby="instance-details">
                <Text as="h2" id="instance-details" size={400} weight="semibold" block>
                  {isGroup ? "Resource details" : "Instance details"}
                </Text>
                <Field
                  label={spec.nameLabel}
                  required
                  orientation="horizontal"
                  className="az-form-field"
                  validationMessage={shown("name") ?? (checking ? "Checking availability…" : undefined)}
                  validationState={shown("name") ? "error" : "none"}
                >
                  <Input value={name} onChange={(_, data) => setName(data.value)} />
                </Field>
                <Field label="Region" required orientation="horizontal" className="az-form-field" validationMessage={shown("location")}>
                  <Dropdown
                    value={locations.data?.find((entry) => entry.name === location)?.regionalDisplayName ?? location}
                    selectedOptions={[location]}
                    onOptionSelect={(_, data) => setLocation(data.optionValue ?? "")}
                  >
                    {(locations.data ?? []).map((entry) => (
                      <Option key={entry.name} value={entry.name}>
                        {entry.regionalDisplayName}
                      </Option>
                    ))}
                  </Dropdown>
                </Field>
                {controls("Basics", "Instance details")}
              </section>
              {fieldsFor("Basics")}
            </>
          ) : tab === "Networking" || tab === "Advanced" ? (
            fieldsFor(tab)
          ) : tab === "Tags" ? (
            <section className="az-form-section" aria-label="Tags">
              <Text block size={200} className="az-form-help">
                Tags are name/value pairs that let you categorize resources and view consolidated billing by applying the same tag to multiple resources and resource groups.
              </Text>
              <div className="az-tags-grid" role="group" aria-label="Tags">
                {tags.map((row, position) => (
                  <div className="az-tags-row" key={position}>
                    <Input aria-label={`Tag ${position + 1} name`} placeholder="Name" value={row.name} onChange={(_, data) => {
                      const next = tags.map((entry, at) => (at === position ? { ...entry, name: data.value } : entry));
                      setTags(position === tags.length - 1 && data.value ? [...next, { name: "", value: "" }] : next);
                    }} />
                    <span aria-hidden>:</span>
                    <Input aria-label={`Tag ${position + 1} value`} placeholder="Value" value={row.value} onChange={(_, data) => setTags(tags.map((entry, at) => (at === position ? { ...entry, value: data.value } : entry)))} />
                    {shown(`tag-${position}`) ? <Text className="az-error-text">{shown(`tag-${position}`)}</Text> : null}
                  </div>
                ))}
              </div>
            </section>
          ) : (
            <section className="az-form-section" aria-label="Review + create">
              {Object.keys(errors).length === 0 ? (
                <InfoBar intent="success" title="Validation passed" testid="validation-passed">
                  Review the settings below, then select Create.
                </InfoBar>
              ) : (
                <ErrorBar title="Validation failed. Required information is missing or not valid." testid="validation-failed">
                  {Array.from(errorTabs).join(", ")}
                </ErrorBar>
              )}
              <dl className="az-review">
                {summary.map(([label, value]) => (
                  <div key={label} className="az-review-row">
                    <dt>{label}</dt>
                    <dd>{value || "—"}</dd>
                  </div>
                ))}
              </dl>
            </section>
          )}
          {submitError ? <ErrorBar title="The deployment could not start.">{submitError}</ErrorBar> : null}
        </div>
        <footer className="az-wizard-footer">
          {tab === "Review + create" ? (
            <Button appearance="primary" onClick={create} disabled={checking} data-testid="wizard-create">
              Create
            </Button>
          ) : (
            <Button
              appearance="primary"
              onClick={() => {
                setShowErrors(true);
                setTab("Review + create");
              }}
              data-testid="wizard-review"
            >
              Review + create
            </Button>
          )}
          <Button disabled={index === 0} onClick={() => setTab(tabs[index - 1])}>
            {"< Previous"}
          </Button>
          {index < tabs.length - 1 ? <Button onClick={() => setTab(tabs[index + 1])}>{`Next : ${tabs[index + 1]} >`}</Button> : null}
          {checking ? <Spinner size="extra-tiny" label="Checking name" /> : null}
        </footer>
      </div>
    </Blade>
  );
}

/** `create/{Namespace}/{type}`. */
export function CreateWizardPage() {
  const { namespace = "", type = "", kind = "" } = useParams();
  const spec = specForRoute(`${namespace}/${type}`, kind);
  if (!spec) {
    return (
      <Blade title="Create" trail={[{ label: "Home", to: "/ui/home" }, { label: "Create" }]}>
        <EmptyState title="The console has no create experience for this resource type." description={`${namespace}/${type}`} />
      </Blade>
    );
  }
  return <Wizard key={spec.id} spec={spec} />;
}
