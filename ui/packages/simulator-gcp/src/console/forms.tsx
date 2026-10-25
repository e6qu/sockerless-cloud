import { cloneElement, useId, useState, type FormEvent, type ReactElement, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { GcpPageHeader } from "./GcpConsole.js";
import { Icon } from "./icons.js";
import { useProjectPath } from "./project.js";

/** One titled section of a create page, as the real console groups a form. */
export function FormSection({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  const id = `section-${title.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`;
  return (
    <section className="gc-form-section" aria-labelledby={id}>
      <h2 id={id} className="gc-form-section-title">{title}</h2>
      {description ? <p className="gc-form-section-description">{description}</p> : null}
      {children}
    </section>
  );
}

export interface EquivalentRequest {
  method: string;
  path: string;
  body?: unknown;
}

// The full-page create form the real console opens from a list's Create
// action: a back arrow, the sections, then Create and Cancel, with
// Equivalent code showing the REST request the form sends.
export function CreatePage({
  title,
  backTo,
  submitLabel = "Create",
  submitting,
  canSubmit,
  error,
  errorTitle,
  onSubmit,
  request,
  testId,
  children,
}: {
  title: string;
  backTo: string;
  submitLabel?: string;
  submitting: boolean;
  canSubmit: boolean;
  error: unknown;
  errorTitle: string;
  onSubmit: () => void;
  request?: EquivalentRequest;
  testId: string;
  children: ReactNode;
}) {
  const navigate = useNavigate();
  const path = useProjectPath();
  const [showCode, setShowCode] = useState(false);
  return (
    <div className="gc-create-page">
      <GcpPageHeader title={title} backTo={backTo} />
      <form
        className="gc-form"
        data-testid={testId}
        noValidate
        onSubmit={(event: FormEvent) => {
          event.preventDefault();
          if (canSubmit && !submitting) onSubmit();
        }}
      >
        {children}
        {error ? (
          <div className="gc-message gc-message-error" role="alert">
            <Icon name="error_fill" />
            <span>
              <strong>{errorTitle}</strong> {error instanceof Error ? error.message : String(error)}
            </span>
          </div>
        ) : null}
        <div className="gc-form-actions">
          <button type="submit" className="gc-button-primary" data-testid={`${testId}-submit`} disabled={!canSubmit || submitting}>
            {submitting ? `${submitLabel.replace(/e$/, "")}ing…` : submitLabel}
          </button>
          <button type="button" className="gc-button-text" onClick={() => navigate(path(backTo))}>
            Cancel
          </button>
          {request ? (
            <button
              type="button"
              className="gc-button-text gc-form-code-toggle"
              aria-expanded={showCode}
              onClick={() => setShowCode((value) => !value)}
            >
              <Icon name="data_object" size="1.25em" />
              Equivalent code
            </button>
          ) : null}
        </div>
        {request && showCode ? (
          <section className="gc-equivalent-code" aria-label="Equivalent REST request">
            <pre className="gc-code">
              {`${request.method} ${request.path}${request.body === undefined ? "" : `\n\n${JSON.stringify(request.body, null, 2)}`}`}
            </pre>
          </section>
        ) : null}
      </form>
    </div>
  );
}

/** A labelled control with its hint or error, the console's outlined field.
 *  The label names the control alone; the hint and the error describe it. */
export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  children: ReactElement<{ id?: string; "aria-describedby"?: string; "aria-invalid"?: boolean }>;
}) {
  const id = useId();
  const noteId = `${id}-note`;
  const note = error ?? hint;
  return (
    <div className="gc-field">
      <label className="gc-field-label" htmlFor={id}>
        {label}
      </label>
      {cloneElement(children, {
        id,
        ...(note ? { "aria-describedby": noteId } : {}),
        ...(error ? { "aria-invalid": true } : {}),
      })}
      {error ? (
        <span className="gc-field-error" id={noteId} role="alert">
          {error}
        </span>
      ) : hint ? (
        <span className="gc-field-hint" id={noteId}>
          {hint}
        </span>
      ) : null}
    </div>
  );
}
