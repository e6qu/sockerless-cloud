/** The console shows a short resource name from a fully-qualified path like
 *  `projects/p/locations/l/jobs/name`. */
export function shortName(name: string): string {
  const parts = name.split("/");
  return parts[parts.length - 1] || name;
}

/** RFC 3339 create timestamps render as a readable local date-time. */
export function formatTimestamp(value: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

/** The JSON APIs (Cloud Storage `size`, Artifact Registry file `sizeBytes`)
 *  report byte counts as decimal strings; the console renders them the way
 *  the real console's Objects/Images tables do, as a human-readable size. */
export function formatBytes(value: string | number | undefined): string {
  if (value === undefined || value === "") return "—";
  const bytes = typeof value === "string" ? Number(value) : value;
  if (!Number.isFinite(bytes)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = bytes;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? size : size.toFixed(size >= 10 ? 0 : 1)} ${units[unit]}`;
}

/** Artifact Registry docker image resource names carry the repository-
 *  relative image path and the content digest joined by "@"
 *  (".../dockerImages/<path>@sha256:..."). The console shows them as two
 *  columns, the way the real Images table does, rather than one long string. */
export function splitImageDigest(name: string): { path: string; digest: string } {
  const marker = "/dockerImages/";
  const index = name.indexOf(marker);
  const rest = index >= 0 ? name.slice(index + marker.length) : name;
  const at = rest.lastIndexOf("@");
  return at >= 0 ? { path: rest.slice(0, at), digest: rest.slice(at + 1) } : { path: rest, digest: "" };
}
