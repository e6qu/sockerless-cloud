// Console routes mirror the portal's own deep links: `#browse/{type}`,
// `#resource{id}/{section}`, `#create/{type}`, under /ui/ as paths.

export function resourceRoute(id: string, section = "overview"): string {
  return `/ui/resource${id}/${section}`;
}

export function createRoute(type: string): string {
  return `/ui/create/${type}`;
}

export const DEPLOYMENT_ROUTE = "/ui/deployment";

/** Splits a resource route's splat into the resource ID and the section path
 *  after it. A provider resource's ID ends at its name, the segment after its
 *  type; a resource group's and a subscription's end at their names. */
export function splitResourcePath(splat: string): { id: string; section: string[] } {
  const segments = splat.split("/").filter(Boolean);
  let end = 2;
  if (segments[2]?.toLowerCase() === "resourcegroups") {
    end = 4;
    if (segments[4]?.toLowerCase() === "providers") end = 8;
  }
  return { id: `/${segments.slice(0, end).join("/")}`, section: segments.slice(end) };
}
