/** Values the console shows as text rather than as raw numbers. */

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exponent;
  return `${value >= 10 || exponent === 0 ? Math.round(value) : value.toFixed(1)} ${units[exponent]}`;
}

/** Epoch seconds or milliseconds, whichever the service reports. */
export function formatEpoch(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "–";
  const milliseconds = value > 1e12 ? value : value * 1000;
  return new Date(milliseconds).toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

export function formatTimestamp(value: string): string {
  if (!value) return "–";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

/** Retention is reported as days, and zero means the service never expires. */
export function formatRetention(days: number): string {
  return !days || days <= 0 ? "Never expire" : `${days} day${days === 1 ? "" : "s"}`;
}
