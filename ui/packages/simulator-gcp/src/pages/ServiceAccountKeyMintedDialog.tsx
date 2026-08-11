import { useEffect, useRef } from "react";
import { GcpDialog } from "../console/GcpDialog.js";
import {
  serviceAccountKeyDataURI,
  serviceAccountKeyFile,
  serviceAccountKeyId,
} from "../console/keyfile.js";
import type { ServiceAccountKey } from "../api.js";

// gcloudUsage is the post-mint CLI recipe: point the CLI at the same cloud
// coordinate the console uses, activate the downloaded key, and make an
// authenticated call. This exact sequence is proven end-to-end by
// TestIAMServiceAccountKeysActivateCLI in simulator-gcp/cli-tests.
export function gcloudUsage(endpoint: string, filename: string): string {
  return [
    `gcloud config set api_endpoint_overrides/iam ${endpoint}/`,
    `gcloud config set auth/token_host ${endpoint}/token`,
    `gcloud auth activate-service-account --key-file=${filename}`,
    "gcloud iam service-accounts list",
  ].join("\n");
}

// The one-time download the real console presents after creating a key: the
// decoded privateKeyData saved as `<project>-<key-id prefix>.json`, with the
// warning that this is the only copy. The download starts on open (as the
// real console's does) unless autoDownload is off.
export function ServiceAccountKeyMintedDialog({
  project,
  saKey,
  endpoint,
  onClose,
  autoDownload = true,
}: {
  project: string;
  saKey: ServiceAccountKey;
  endpoint: string;
  onClose: () => void;
  autoDownload?: boolean;
}) {
  const anchor = useRef<HTMLAnchorElement>(null);
  useEffect(() => {
    if (autoDownload) anchor.current?.click();
  }, [autoDownload]);

  const file = serviceAccountKeyFile(project, saKey);
  if (!file) {
    // The API returns privateKeyData only on create; a key without it has
    // nothing to download and the dialog must say so rather than save an
    // empty file.
    return (
      <GcpDialog title="Private key unavailable" testId="sa-key-minted-dialog" onClose={onClose}>
        <p className="gc-message gc-message-error" role="alert">
          This key's private material was returned only when it was created and can't be downloaded
          again.
        </p>
        <div className="gc-dialog-actions">
          <button type="button" className="gc-button-primary" data-testid="sa-key-minted-done" onClick={onClose}>
            Close
          </button>
        </div>
      </GcpDialog>
    );
  }

  return (
    <GcpDialog title="Private key saved to your computer" testId="sa-key-minted-dialog" onClose={onClose}>
      <p data-testid="sa-key-filename">
        <code>{file.filename}</code> allows access to your cloud resources, so store it securely.{" "}
        <strong className="gc-danger">
          This is the only copy — the key can't be downloaded again.
        </strong>
      </p>
      <p className="gc-derived-email">Key ID: {serviceAccountKeyId(saKey)}</p>
      <h3 className="gc-detail-heading">Use with the Google Cloud CLI</h3>
      <pre className="gc-code" data-testid="sa-key-cli">{gcloudUsage(endpoint, file.filename)}</pre>
      <div className="gc-dialog-actions">
        <a
          ref={anchor}
          className="gc-button-text"
          data-testid="sa-key-download"
          href={serviceAccountKeyDataURI(file)}
          download={file.filename}
        >
          Download again
        </a>
        <button type="button" className="gc-button-primary" data-testid="sa-key-minted-done" onClick={onClose}>
          Done
        </button>
      </div>
    </GcpDialog>
  );
}
