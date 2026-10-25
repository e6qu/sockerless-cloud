import { useRef, useState } from "react";
import { Link as RouterLink, useNavigate } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  Dropdown,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
  Option,
  OverlayDrawer,
  Tab,
  TabList,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import type { ArmGenericResource } from "../../api.js";
import { getBlobCorsRules, setBlobCorsRules, type CorsRule } from "../../azure.js";
import { createContainer, deleteBlob, deleteContainer, downloadBlob, listBlobs, listContainers, uploadBlob } from "../../storage.js";
import { armText } from "../../portal/format.js";
import { CommandBar, ConfirmDialog, EmptyState, ErrorBar, errorText, formatTime, InfoBar } from "../../portal/AzurePortal.js";
import { AzureIcon } from "../../portal/icons.js";
import { useNotifications } from "../../portal/notifications.js";
import { resourceRoute } from "../../portal/routes.js";

const CONTAINER_NAME = /^(?!.*--)[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;
const ACCESS_LEVELS = [
  { value: "None", label: "Private (no anonymous access)" },
  { value: "Blob", label: "Blob (anonymous read access for blobs only)" },
  { value: "Container", label: "Container (anonymous read access for containers and blobs)" },
];

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
}

export function ContainersSection({ account, path }: { account: ArmGenericResource; path: string[] }) {
  const container = path[0];
  return container ? <BlobsView account={account} container={decodeURIComponent(container)} /> : <ContainerList account={account} />;
}

function ContainerList({ account }: { account: ArmGenericResource }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const key = ["containers", account.id];
  const containers = useQuery({ queryKey: key, queryFn: () => listContainers(account.id) });
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [access, setAccess] = useState("None");
  const [selected, setSelected] = useState<string[]>([]);
  const [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [filter, setFilter] = useState("");
  const nameError = name && !CONTAINER_NAME.test(name) ? "Container names must be 3 to 63 lowercase letters, numbers or hyphens, start with a letter or number, and not hold two hyphens in a row." : undefined;
  const refresh = () => void queryClient.invalidateQueries({ queryKey: key });
  const create = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: `Creating container ${name}`, success: `Created container ${name}`, failure: `Failed to create container ${name}`, description: account.name }, () => createContainer(account.id, name, access));
      setCreating(false);
      setName("");
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: `Deleting ${selected.length} containers`, success: `Deleted ${selected.length} containers`, failure: "Failed to delete containers", description: selected.join(", ") }, () =>
        Promise.all(selected.map((entry) => deleteContainer(account.id, entry))),
      );
      setSelected([]);
      setDeleting(false);
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const rows = (containers.data ?? []).filter((entry) => entry.name.includes(filter.toLowerCase()));
  return (
    <section aria-label="Containers" className="az-section" data-testid="containers">
      <CommandBar
        commands={[
          { label: "Container", icon: "add", onSelect: () => setCreating(true), testid: "container-create" },
          { label: "Refresh", icon: "refresh", onSelect: refresh },
          { label: "Delete", icon: "delete", disabled: selected.length === 0, onSelect: () => setDeleting(true), testid: "container-delete" },
        ]}
      />
      <Input className="az-filter-text" value={filter} onChange={(_, data) => setFilter(data.value)} placeholder="Search containers by prefix" aria-label="Search containers by prefix" contentBefore={<AzureIcon name="search" size={14} />} />
      {containers.isError ? <ErrorBar title="The containers could not be listed.">{errorText(containers.error)}</ErrorBar> : null}
      <Table aria-label="Containers" size="small" className="az-grid" data-testid="containers-table">
        <TableHeader>
          <TableRow>
            <TableHeaderCell className="az-grid-select"><span className="sr-only">Select</span></TableHeaderCell>
            <TableHeaderCell>Name</TableHeaderCell>
            <TableHeaderCell>Last modified</TableHeaderCell>
            <TableHeaderCell>Anonymous access level</TableHeaderCell>
            <TableHeaderCell>Lease state</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((entry) => (
            <TableRow key={entry.id}>
              <TableCell className="az-grid-select">
                <Checkbox aria-label={`Select ${entry.name}`} checked={selected.includes(entry.name)} onChange={(_, data) => setSelected(data.checked ? [...selected, entry.name] : selected.filter((value) => value !== entry.name))} />
              </TableCell>
              <TableCell>
                <RouterLink to={resourceRoute(account.id, `containers/${encodeURIComponent(entry.name)}`)}>{entry.name}</RouterLink>
              </TableCell>
              <TableCell>{formatTime(armText(entry.properties, "lastModifiedTime"))}</TableCell>
              <TableCell>{ACCESS_LEVELS.find((level) => level.value === armText(entry.properties, "publicAccess"))?.label.split(" (")[0] ?? "Private"}</TableCell>
              <TableCell>{armText(entry.properties, "leaseState") || "Available"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {containers.isLoading ? <EmptyState title="Loading containers…" loading /> : null}
      {containers.data && rows.length === 0 ? <EmptyState title="No containers found" description="Create a container to store blobs." /> : null}
      <OverlayDrawer open={creating} position="end" size="small" onOpenChange={(_, data) => !data.open && setCreating(false)} aria-label="New container">
        <DrawerHeader>
          <DrawerHeaderTitle action={<Button appearance="subtle" aria-label="Close" icon={<AzureIcon name="dismiss" />} onClick={() => setCreating(false)} />}>New container</DrawerHeaderTitle>
        </DrawerHeader>
        <DrawerBody className="az-drawer-form">
          <Field label="Name" required validationMessage={nameError} validationState={nameError ? "error" : "none"}>
            <Input value={name} onChange={(_, data) => setName(data.value)} />
          </Field>
          <Field label="Anonymous access level">
            <Dropdown value={ACCESS_LEVELS.find((level) => level.value === access)?.label} selectedOptions={[access]} onOptionSelect={(_, data) => setAccess(data.optionValue ?? "None")}>
              {ACCESS_LEVELS.map((level) => (
                <Option key={level.value} value={level.value}>
                  {level.label}
                </Option>
              ))}
            </Dropdown>
          </Field>
          {error ? <ErrorBar title="The container could not be created.">{error}</ErrorBar> : null}
        </DrawerBody>
        <DrawerFooter>
          <Button appearance="primary" disabled={!name || Boolean(nameError) || busy} onClick={() => void create()} data-testid="container-submit">
            {busy ? "Creating…" : "Create"}
          </Button>
          <Button onClick={() => setCreating(false)}>Cancel</Button>
        </DrawerFooter>
      </OverlayDrawer>
      <ConfirmDialog open={deleting} title="Delete container(s)" busy={busy} testid="container-delete-dialog" error={deleting ? error : undefined} onConfirm={() => void remove()} onCancel={() => setDeleting(false)}>
        <Text block>Do you want to delete the selected containers? Every blob in them is deleted too.</Text>
      </ConfirmDialog>
    </section>
  );
}

function BlobsView({ account, container }: { account: ArmGenericResource; container: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { track } = useNotifications();
  const endpoint = armText(account.properties, "primaryEndpoints.blob");
  const key = ["blobs", account.id, container];
  const blobs = useQuery({ queryKey: key, queryFn: () => listBlobs(account.id, endpoint, container), retry: false });
  const [selected, setSelected] = useState<string[]>([]);
  const [uploading, setUploading] = useState(false);
  const [files, setFiles] = useState<File[]>([]);
  const [overwrite, setOverwrite] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [deleting, setDeleting] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const refresh = () => void queryClient.invalidateQueries({ queryKey: key });

  const upload = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: `Uploading ${files.length} blob(s)`, success: `Uploaded ${files.length} blob(s)`, failure: "Upload failed", description: `${account.name}/${container}` }, async () => {
        for (const file of files) await uploadBlob(account.id, endpoint, container, file, overwrite);
      });
      setUploading(false);
      setFiles([]);
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const download = async () => {
    setError(undefined);
    try {
      for (const name of selected) {
        const blob = await downloadBlob(account.id, endpoint, container, name);
        const link = document.createElement("a");
        link.href = URL.createObjectURL(blob);
        link.download = name.split("/").pop() ?? name;
        link.click();
        URL.revokeObjectURL(link.href);
      }
    } catch (err) {
      setError(errorText(err));
    }
  };
  const remove = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: `Deleting ${selected.length} blob(s)`, success: `Deleted ${selected.length} blob(s)`, failure: "Delete failed", description: selected.join(", ") }, () =>
        Promise.all(selected.map((name) => deleteBlob(account.id, endpoint, container, name))),
      );
      setSelected([]);
      setDeleting(false);
      refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label={`Container ${container}`} className="az-section" data-testid="blobs">
      <div className="az-subnav">
        <Button appearance="transparent" size="small" icon={<AzureIcon name="chevron_right" />} onClick={() => navigate(resourceRoute(account.id, "containers"))}>
          Containers
        </Button>
        <Text weight="semibold">{container}</Text>
      </div>
      <CommandBar
        commands={[
          { label: "Upload", icon: "upload", onSelect: () => setUploading(true), testid: "blob-upload" },
          { label: "Refresh", icon: "refresh", onSelect: refresh },
          { label: "Download", icon: "download", disabled: selected.length === 0, onSelect: () => void download(), testid: "blob-download" },
          { label: "Delete", icon: "delete", disabled: selected.length === 0, onSelect: () => setDeleting(true), testid: "blob-delete" },
        ]}
      />
      <Text size={200} block>
        Authentication method: Access key · Location: {container}
      </Text>
      {blobs.isError ? (
        <ErrorBar title="The blobs could not be listed." testid="blobs-error">
          {errorText(blobs.error)} <RouterLink to={resourceRoute(account.id, "cors")}>Resource sharing (CORS)</RouterLink>
        </ErrorBar>
      ) : null}
      {error && !deleting ? <ErrorBar title="The operation failed.">{error}</ErrorBar> : null}
      <Table aria-label="Blobs" size="small" className="az-grid" data-testid="blobs-table">
        <TableHeader>
          <TableRow>
            <TableHeaderCell className="az-grid-select"><span className="sr-only">Select</span></TableHeaderCell>
            <TableHeaderCell>Name</TableHeaderCell>
            <TableHeaderCell>Modified</TableHeaderCell>
            <TableHeaderCell>Access tier</TableHeaderCell>
            <TableHeaderCell>Blob type</TableHeaderCell>
            <TableHeaderCell>Size</TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(blobs.data ?? []).map((blob) => (
            <TableRow key={blob.name}>
              <TableCell className="az-grid-select">
                <Checkbox aria-label={`Select ${blob.name}`} checked={selected.includes(blob.name)} onChange={(_, data) => setSelected(data.checked ? [...selected, blob.name] : selected.filter((value) => value !== blob.name))} />
              </TableCell>
              <TableCell>{blob.name}</TableCell>
              <TableCell>{formatTime(blob.lastModified)}</TableCell>
              <TableCell>{blob.accessTier || "—"}</TableCell>
              <TableCell>{blob.blobType}</TableCell>
              <TableCell>{formatBytes(blob.contentLength)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {blobs.isLoading ? <EmptyState title="Loading blobs…" loading /> : null}
      {blobs.data && blobs.data.length === 0 ? <EmptyState title="No blobs found." description="Upload a file to add a blob." /> : null}
      <OverlayDrawer open={uploading} position="end" size="small" onOpenChange={(_, data) => !data.open && setUploading(false)} aria-label="Upload blob">
        <DrawerHeader>
          <DrawerHeaderTitle action={<Button appearance="subtle" aria-label="Close" icon={<AzureIcon name="dismiss" />} onClick={() => setUploading(false)} />}>Upload blob</DrawerHeaderTitle>
        </DrawerHeader>
        <DrawerBody className="az-drawer-form">
          <input ref={fileInput} type="file" multiple className="az-file-input" aria-label="Files to upload" data-testid="blob-file" onChange={(event) => setFiles(Array.from(event.target.files ?? []))} />
          <Button icon={<AzureIcon name="folder" />} onClick={() => fileInput.current?.click()}>
            Browse for files
          </Button>
          <Text block size={200}>
            {files.length ? files.map((file) => file.name).join(", ") : "No files selected."}
          </Text>
          <Checkbox label="Overwrite if files already exist" checked={overwrite} onChange={(_, data) => setOverwrite(Boolean(data.checked))} />
          {error ? <ErrorBar title="The upload failed.">{error}</ErrorBar> : null}
        </DrawerBody>
        <DrawerFooter>
          <Button appearance="primary" disabled={files.length === 0 || busy} onClick={() => void upload()} data-testid="blob-upload-submit">
            {busy ? "Uploading…" : "Upload"}
          </Button>
          <Button onClick={() => setUploading(false)}>Cancel</Button>
        </DrawerFooter>
      </OverlayDrawer>
      <ConfirmDialog open={deleting} title="Delete blob(s)" busy={busy} testid="blob-delete-dialog" error={deleting ? error : undefined} onConfirm={() => void remove()} onCancel={() => setDeleting(false)}>
        <Text block>Do you want to delete the selected blobs?</Text>
      </ConfirmDialog>
    </section>
  );
}

const METHODS = ["DELETE", "GET", "HEAD", "MERGE", "POST", "OPTIONS", "PUT", "PATCH"];

interface RuleDraft {
  origins: string;
  methods: string[];
  headers: string;
  exposed: string;
  maxAge: string;
}

function draftOf(rule: CorsRule): RuleDraft {
  return { origins: rule.allowedOrigins.join(","), methods: rule.allowedMethods, headers: rule.allowedHeaders.join(","), exposed: rule.exposedHeaders.join(","), maxAge: String(rule.maxAgeInSeconds) };
}

const split = (text: string) => text.split(",").map((entry) => entry.trim()).filter(Boolean);

/** Resource sharing (CORS): the Blob service's CORS rules, which decide the
 *  browser origins that may call the account's Blob endpoint. */
export function CorsSection({ account }: { account: ArmGenericResource }) {
  const queryClient = useQueryClient();
  const { track } = useNotifications();
  const rules = useQuery({ queryKey: ["cors", account.id], queryFn: () => getBlobCorsRules(account.id) });
  const [draft, setDraft] = useState<RuleDraft[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const empty: RuleDraft = { origins: "", methods: [], headers: "", exposed: "", maxAge: "0" };
  const rows = [...(draft ?? (rules.data ?? []).map(draftOf)), empty];
  const setRow = (index: number, change: Partial<RuleDraft>) => {
    const current = draft ?? (rules.data ?? []).map(draftOf);
    const next = index === current.length ? [...current, { ...empty, ...change }] : current.map((row, position) => (position === index ? { ...row, ...change } : row));
    setDraft(next);
  };
  const invalid = (draft ?? []).some((row) => !split(row.origins).length || row.methods.length === 0 || !/^\d+$/.test(row.maxAge));
  const save = async () => {
    if (!draft) return;
    setBusy(true);
    setError(undefined);
    try {
      await track({ progress: "Saving CORS settings", success: "Saved CORS settings", failure: "Failed to save CORS settings", description: account.name }, () =>
        setBlobCorsRules(
          account.id,
          draft.map((row) => ({ allowedOrigins: split(row.origins), allowedMethods: row.methods, allowedHeaders: split(row.headers), exposedHeaders: split(row.exposed), maxAgeInSeconds: Number(row.maxAge) })),
        ),
      );
      setDraft(null);
      void queryClient.invalidateQueries({ queryKey: ["cors", account.id] });
      void queryClient.invalidateQueries({ queryKey: ["blobs", account.id] });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label="Resource sharing (CORS)" className="az-section" data-testid="cors">
      <CommandBar
        commands={[
          { label: "Save", icon: "document", disabled: !draft || invalid || busy, onSelect: () => void save(), testid: "cors-save" },
          { label: "Discard", icon: "dismiss", disabled: !draft || busy, onSelect: () => setDraft(null) },
        ]}
      />
      <InfoBar>
        Cross-origin resource sharing (CORS) lets a web page on one origin call the Blob service. This console reaches blobs from <code>{window.location.origin}</code>, so an allowed origin must match it.
      </InfoBar>
      <TabList selectedValue="blob" aria-label="Storage services">
        <Tab value="blob">Blob service</Tab>
      </TabList>
      {rules.isError ? <ErrorBar title="The CORS rules could not be read.">{errorText(rules.error)}</ErrorBar> : null}
      {error ? <ErrorBar title="The CORS rules could not be saved.">{error}</ErrorBar> : null}
      <Table aria-label="CORS rules" size="small" className="az-grid az-cors-grid" data-testid="cors-rules">
        <TableHeader>
          <TableRow>
            <TableHeaderCell>Allowed origins</TableHeaderCell>
            <TableHeaderCell>Allowed methods</TableHeaderCell>
            <TableHeaderCell>Allowed headers</TableHeaderCell>
            <TableHeaderCell>Exposed headers</TableHeaderCell>
            <TableHeaderCell>Max age</TableHeaderCell>
            <TableHeaderCell><span className="sr-only">Delete</span></TableHeaderCell>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row, index) => (
            <TableRow key={index}>
              <TableCell>
                <Input aria-label={`Rule ${index + 1} allowed origins`} value={row.origins} onChange={(_, data) => setRow(index, { origins: data.value })} />
              </TableCell>
              <TableCell>
                <Dropdown
                  multiselect
                  aria-label={`Rule ${index + 1} allowed methods`}
                  placeholder="0 selected"
                  value={row.methods.length ? `${row.methods.length} selected` : ""}
                  selectedOptions={row.methods}
                  onOptionSelect={(_, data) => setRow(index, { methods: data.selectedOptions })}
                >
                  {METHODS.map((method) => (
                    <Option key={method} value={method}>
                      {method}
                    </Option>
                  ))}
                </Dropdown>
              </TableCell>
              <TableCell>
                <Input aria-label={`Rule ${index + 1} allowed headers`} value={row.headers} onChange={(_, data) => setRow(index, { headers: data.value })} />
              </TableCell>
              <TableCell>
                <Input aria-label={`Rule ${index + 1} exposed headers`} value={row.exposed} onChange={(_, data) => setRow(index, { exposed: data.value })} />
              </TableCell>
              <TableCell>
                <Input aria-label={`Rule ${index + 1} max age`} value={row.maxAge} onChange={(_, data) => setRow(index, { maxAge: data.value })} />
              </TableCell>
              <TableCell>
                {index < rows.length - 1 ? (
                  <Button
                    appearance="subtle"
                    aria-label={`Delete rule ${index + 1}`}
                    icon={<AzureIcon name="delete" />}
                    onClick={() => setDraft((draft ?? (rules.data ?? []).map(draftOf)).filter((_, position) => position !== index))}
                  />
                ) : null}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  );
}
