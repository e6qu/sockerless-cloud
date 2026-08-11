/** Azure resource IDs are long; the portal shows the resource group and name
 *  from them rather than the whole path. */
export function resourceGroupOf(id: string): string {
  const match = /\/resourceGroups\/([^/]+)/i.exec(id);
  return match ? match[1] : "—";
}

export function locationLabel(location: string): string {
  return location || "—";
}

/** Reads a dotted path out of an Azure Resource Manager `properties` object
 *  and renders it the way the portal shows it: a scalar as text, a list of
 *  scalars comma-joined, and anything absent as the empty string (so a caller
 *  can choose its own placeholder). The paths the service blades pass are the
 *  exact property names the resource's Azure REST specification documents —
 *  `addressSpace.addressPrefixes` on a virtual network,
 *  `consistencyPolicy.defaultConsistencyLevel` on a Cosmos DB account — so
 *  this reads the true response shape rather than a remapped one. */
export function armText(properties: Record<string, unknown>, path: string): string {
  let current: unknown = properties;
  for (const segment of path.split(".")) {
    if (current === null || typeof current !== "object") return "";
    current = (current as Record<string, unknown>)[segment];
  }
  if (current === null || current === undefined) return "";
  if (Array.isArray(current)) {
    return current
      .map((entry) => (entry === null || typeof entry === "object" ? JSON.stringify(entry) : String(entry)))
      .join(", ");
  }
  if (typeof current === "object") return JSON.stringify(current);
  return String(current);
}

/** The portal renders a resource's tags map as the real portal's Essentials
 *  line does — `key : value` pairs, or a dash when the resource has none. */
export function tagsSummary(tags: Record<string, string>): string {
  const entries = Object.entries(tags);
  if (entries.length === 0) return "—";
  return entries.map(([key, value]) => (value ? `${key} : ${value}` : key)).join(", ");
}
