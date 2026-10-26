import { useEffect, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import Button from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Badge from "@cloudscape-design/components/badge";
import StatusIndicator, { type StatusIndicatorProps } from "@cloudscape-design/components/status-indicator";
import Modal, { type ModalProps } from "@cloudscape-design/components/modal";
import CopyToClipboard from "@cloudscape-design/components/copy-to-clipboard";
import KeyValuePairs, { type KeyValuePairsProps } from "@cloudscape-design/components/key-value-pairs";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Spinner from "@cloudscape-design/components/spinner";
import SpaceBetween from "@cloudscape-design/components/space-between";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Link from "@cloudscape-design/components/link";
import { useOpenHelp } from "./AwsApp.js";
import { currentRegion } from "./federation.js";
import { serviceForPath } from "./serviceCatalog.js";

/**
 * The console's shared building blocks — real `@cloudscape-design/components`,
 * the design system the AWS Management Console is built on — in the shapes the
 * service pages consume.
 */

/** The browser tab title the real console sets: page, service, Region. */
export function usePageTitle(title: string) {
  const { pathname } = useLocation();
  const serviceName = serviceForPath(pathname)?.name;
  useEffect(() => {
    if (!title) return;
    document.title = [title, serviceName && serviceName !== title ? serviceName : "", currentRegion()].filter(Boolean).join(" | ");
  }, [title, serviceName]);
}

/** The "Info" link the real console puts beside a page heading; it opens the
 * help panel. */
export function AwsInfoLink() {
  const openHelp = useOpenHelp();
  return (
    <Link variant="info" onFollow={() => openHelp()}>
      Info
    </Link>
  );
}

/**
 * `Resources (count)` with actions — the heading every Cloudscape page opens
 * with, which also sets the browser tab title.
 */
export function AwsPageHeader({
  title,
  count,
  description,
  actions,
}: {
  title: string;
  count?: number;
  description?: string;
  actions?: ReactNode;
}) {
  usePageTitle(title);
  return (
    <Header
      variant="h1"
      description={description}
      actions={actions}
      info={<AwsInfoLink />}
      counter={count !== undefined ? `(${count})` : undefined}
    >
      {title}
    </Header>
  );
}

/**
 * The real console's delete confirmation: the dialog names what goes, warns
 * that it is permanent, and keeps Delete disabled until the operator types the
 * confirmation text into the field.
 */
export function AwsDeleteModal({
  title,
  confirmationText = "confirm",
  onDismiss,
  onDelete,
  pending,
  error,
  children,
  testIdPrefix,
}: {
  title: string;
  confirmationText?: string;
  onDismiss: () => void;
  onDelete: () => void;
  pending: boolean;
  error?: ReactNode;
  children: ReactNode;
  testIdPrefix?: string;
}) {
  const [typed, setTyped] = useState("");
  const confirmed = typed === confirmationText;
  return (
    <Modal
      visible
      header={title}
      onDismiss={onDismiss}
      closeAriaLabel="Close"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={onDismiss}>
              Cancel
            </Button>
            <Button
              variant="primary"
              disabled={!confirmed || pending}
              loading={pending}
              onClick={onDelete}
              nativeButtonAttributes={testIdPrefix ? { "data-testid": `${testIdPrefix}-confirm` } : undefined}
            >
              Delete
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="m">
        {children}
        <Alert type="warning">This action can&apos;t be undone.</Alert>
        <FormField label={`To confirm deletion, type "${confirmationText}" in the field.`}>
          <Input
            value={typed}
            placeholder={confirmationText}
            ariaLabel={`Type ${confirmationText} to confirm deletion`}
            onChange={(event) => setTyped(event.detail.value)}
            nativeInputAttributes={testIdPrefix ? { "data-testid": `${testIdPrefix}-confirm-input` } : undefined}
          />
        </FormField>
        {error}
      </SpaceBetween>
    </Modal>
  );
}

/** A long identifier the real console prints with a copy control beside it. */
export function AwsCopyableText({ value, label }: { value: string; label: string }) {
  if (!value) return <>–</>;
  return (
    <CopyToClipboard
      variant="inline"
      textToCopy={value}
      copyButtonAriaLabel={label}
      copySuccessText={`${label.replace(/^Copy /, "")} copied`}
      copyErrorText="Copy failed"
    />
  );
}

export function AwsButton({
  children,
  variant = "normal",
  onClick,
  disabled,
  loading,
  "data-testid": testId,
}: {
  children: ReactNode;
  variant?: "normal" | "primary" | "link";
  onClick?: () => void;
  disabled?: boolean;
  loading?: boolean;
  "data-testid"?: string;
}) {
  return (
    <Button
      variant={variant}
      disabled={disabled}
      loading={loading}
      onClick={onClick ? () => onClick() : undefined}
      nativeButtonAttributes={testId ? { "data-testid": testId } : undefined}
    >
      {children}
    </Button>
  );
}

/** The icon-only refresh control the real console puts first in a table's
 * header actions. */
export function AwsRefreshButton({ onClick, loading }: { onClick: () => void; loading: boolean }) {
  return <Button iconName="refresh" ariaLabel="Refresh" loading={loading} onClick={() => onClick()} />;
}

export function AwsContainer({ children }: { children: ReactNode }) {
  return <Container>{children}</Container>;
}

/**
 * A table cell that navigates to a resource's detail route — every list
 * page's row-header column. A real Cloudscape `Link`, routed through
 * react-router the same way the console's other navigation controls are.
 */
export function AwsRowLink({ to, children }: { to: string; children: ReactNode }) {
  const navigate = useNavigate();
  return (
    <Link
      href={to}
      onFollow={(event) => {
        event.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </Link>
  );
}

/** The pill a service this console has no page for carries on its own page. */
export function AwsNotSupportedBadge({ serviceName }: { serviceName: string }) {
  return (
    <Badge color="grey" nativeAttributes={{ "aria-label": `${serviceName} has no page in this console` }}>
      Not supported
    </Badge>
  );
}

/**
 * A Cloudscape `Modal`: it already traps focus, closes on Escape, on an
 * overlay click, and on its own close control — the same contract the real
 * console's "Access key created" dialog holds (an operator can dismiss it
 * early and lose the one-time secret, exactly as the real console allows),
 * so every caller wires the same handler to `onDismiss` as to any Cancel/Done
 * footer action. Cloudscape's own focus lock moves focus to the first
 * focusable element in the dialog on open — structurally, the dialog's own
 * close control, ahead of any form field in the body — and returns it to
 * whatever opened the dialog on close; this is Cloudscape's real behaviour,
 * not something a caller can retarget to a specific field.
 */
export function AwsModal({
  title,
  onDismiss,
  footer,
  size,
  children,
}: {
  title: string;
  onDismiss: () => void;
  footer: ReactNode;
  size?: ModalProps.Size;
  children: ReactNode;
}) {
  return (
    <Modal
      visible
      header={title}
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            {footer}
          </SpaceBetween>
        </Box>
      }
      size={size}
      onDismiss={onDismiss}
      closeAriaLabel="Close"
    >
      <SpaceBetween size="m">{children}</SpaceBetween>
    </Modal>
  );
}

/**
 * Copy-to-clipboard with spoken and visible feedback, the control Cloudscape
 * places beside every credential and identifier.
 */
export function AwsCopyButton({ value, label }: { value: string; label: string }) {
  return (
    <CopyToClipboard
      variant="icon"
      textToCopy={value}
      copyButtonAriaLabel={label}
      copySuccessText="Copied"
      copyErrorText="Copy failed"
    />
  );
}

export type AwsStatusKind = "success" | "error" | "warning" | "inactive";

/** An API's SCREAMING_SNAKE_CASE state the way the real console prints it:
 * IN_PROGRESS reads "In progress". Any other spelling stays as the API sent it. */
export function statusLabel(status: string): string {
  if (!/^[A-Z][A-Z_]*$/.test(status)) return status;
  const words = status.toLowerCase().replace(/_/g, " ");
  return words[0].toUpperCase() + words.slice(1);
}

const KIND_TO_INDICATOR: Record<AwsStatusKind, StatusIndicatorProps.Type> = {
  success: "success",
  error: "error",
  warning: "in-progress",
  inactive: "stopped",
};

/**
 * Where the caller knows the meaning it passes `kind`, because inferring it
 * from wording is guesswork that fails quietly. The inference below exists
 * only for values that arrive from the service as free text.
 */
export function AwsStatus({ status, kind: explicitKind }: { status: string; kind?: AwsStatusKind }) {
  const value = status.toLowerCase();
  // Failure terms are tested first and matched on whole words. Substring tests
  // are the trap here: "unavailable" contains "available", and "inactive"
  // contains "active", so a looser check reports a green tick for a failure.
  const words = value.split(/[^a-z]+/).filter(Boolean);
  const has = (...candidates: string[]) => candidates.some((candidate) => words.includes(candidate));
  const kind: AwsStatusKind = has(
    "unavailable",
    "stopped",
    "stopping",
    "failed",
    "failure",
    "error",
    "deactivated",
    "inactive",
    "deleted",
  )
    ? "error"
    : has("pending", "provisioning", "creating", "updating", "deleting")
      ? "warning"
      : has("running", "active", "activated", "available", "succeeded", "success", "ok", "healthy")
        ? "success"
        : "inactive";
  const resolved = explicitKind ?? kind;
  return <StatusIndicator type={KIND_TO_INDICATOR[resolved]}>{statusLabel(status)}</StatusIndicator>;
}

/**
 * A resource's key-value property block — Cloudscape's own `KeyValuePairs`.
 * Callers build a flat list of `{label, value}` pairs, skipping a conditional
 * field simply by not including it.
 */
export function AwsKeyValue({
  items,
  columns = 3,
  ariaLabel,
}: {
  items: { label: string; value: ReactNode }[];
  columns?: number;
  ariaLabel?: string;
}) {
  const pairs: KeyValuePairsProps.Item[] = items.map((item) => ({ type: "pair", label: item.label, value: item.value }));
  return <KeyValuePairs columns={columns} items={pairs} ariaLabel={ariaLabel} />;
}

/**
 * A load failure, rendered as a real Cloudscape `Alert`. The `role="alert"`
 * wrapper is this console's own addition on top of Cloudscape's own markup
 * (Cloudscape's Alert relies on its visual presentation and does not itself
 * assert an ARIA live-region role) so a screen reader announces a failed
 * read the moment it happens, matching this console's accessibility bar.
 */
export function AwsErrorAlert({
  children,
  testId,
}: {
  children: ReactNode;
  testId?: string;
}) {
  return (
    <div role="alert" data-testid={testId}>
      <Alert type="error">{children}</Alert>
    </div>
  );
}

/**
 * A resource's empty or loading placeholder. `role="status"` while loading
 * lets assistive tech pick up the change without the operator having to go
 * looking for it.
 */
export function AwsEmptyState({
  title,
  description,
  loading,
  action,
}: {
  title: string;
  description?: string;
  loading?: boolean;
  action?: ReactNode;
}) {
  const body = (
    <Box textAlign="center" color="inherit" padding={{ vertical: "l" }}>
      {loading && (
        <Box margin={{ bottom: "s" }}>
          <Spinner size="normal" />
        </Box>
      )}
      <Box variant="strong" color="inherit">
        {title}
      </Box>
      {description && (
        <Box variant="p" color="inherit" padding={{ bottom: action ? "s" : "n" }}>
          {description}
        </Box>
      )}
      {action}
    </Box>
  );
  return loading ? <div role="status">{body}</div> : body;
}
