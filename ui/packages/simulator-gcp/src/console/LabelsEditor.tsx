import { Icon } from "./icons.js";

// Google Cloud resources carry flat string→string maps the real console edits
// with a key/value row editor — a resource's `labels`, a function's
// `environmentVariables`. This is that editor, reused wherever an edit flow
// touches such a map. It is controlled: the caller holds the rows as state and
// converts them to a map with `pairsToLabels` on submit. The visible noun is
// configurable so the same control reads correctly for labels and for
// environment variables.

export interface LabelPair {
  key: string;
  value: string;
}

export function labelsToPairs(labels?: Record<string, string>): LabelPair[] {
  return Object.entries(labels ?? {}).map(([key, value]) => ({ key, value }));
}

// pairsToLabels drops blank keys and trims them, so a half-typed row never
// becomes a `""` key the API would reject.
export function pairsToLabels(pairs: LabelPair[]): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const { key, value } of pairs) {
    const trimmed = key.trim();
    if (trimmed) labels[trimmed] = value;
  }
  return labels;
}

export function LabelsEditor({
  pairs,
  onChange,
  idPrefix,
  title = "Labels",
  addLabel = "Add label",
  entryNoun = "Label",
}: {
  pairs: LabelPair[];
  onChange: (pairs: LabelPair[]) => void;
  /** Namespaces the row test IDs so several editors on one page stay distinct. */
  idPrefix: string;
  /** The heading over the editor (e.g. "Labels", "Environment variables"). */
  title?: string;
  /** The add-row button's label. */
  addLabel?: string;
  /** Capitalised singular used in each row's accessible names. */
  entryNoun?: string;
}) {
  const update = (index: number, patch: Partial<LabelPair>) =>
    onChange(pairs.map((pair, i) => (i === index ? { ...pair, ...patch } : pair)));
  const remove = (index: number) => onChange(pairs.filter((_, i) => i !== index));
  const add = () => onChange([...pairs, { key: "", value: "" }]);

  return (
    <div className="gc-labels-editor" data-testid={`${idPrefix}-labels`}>
      <span className="gc-field">{title}</span>
      {pairs.length === 0 ? (
        <p className="gc-field-hint">None set.</p>
      ) : (
        <ul className="gc-labels-list">
          {pairs.map((pair, index) => (
            <li className="gc-label-row" key={index}>
              <input
                type="text"
                aria-label={`${entryNoun} ${index + 1} key`}
                data-testid={`${idPrefix}-label-key-${index}`}
                placeholder="key"
                value={pair.key}
                onChange={(event) => update(index, { key: event.target.value })}
              />
              <input
                type="text"
                aria-label={`${entryNoun} ${index + 1} value`}
                data-testid={`${idPrefix}-label-value-${index}`}
                placeholder="value"
                value={pair.value}
                onChange={(event) => update(index, { value: event.target.value })}
              />
              <button
                type="button"
                className="gc-icon-button gc-label-remove"
                aria-label={`Remove ${entryNoun.toLowerCase()} ${pair.key || index + 1}`}
                data-testid={`${idPrefix}-label-remove-${index}`}
                onClick={() => remove(index)}
              >
                <Icon name="close" size="1.25em" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <button
        type="button"
        className="gc-button-text gc-label-add"
        data-testid={`${idPrefix}-label-add`}
        onClick={add}
      >
        <Icon name="add" size="1.25em" />
        {addLabel}
      </button>
    </div>
  );
}
