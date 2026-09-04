#!/usr/bin/env bash
# Regenerates simulator-gcp/compute_interconnect_remote_locations_vendored.json
# from Google's published Cross-Cloud Interconnect location documentation.
#
# The catalogue of third-party facilities Google peers with is Google's to
# publish, so it is vendored — the same practice as the colocation facilities
# beside it and Application Gateway's managed WAF rule sets on the Azure slice.
#
# There is no single list: the remote locations are documented per cloud
# provider, one "Choose your locations" page each, and all four share the same
# six-column table. The tables lean on rowspans — a metropolitan area written
# once covers the entries beneath it — so they are read through the grid
# expander in scripts/lib/html_table_grid.py rather than row by row. A
# row-counting parser gets the enumeration right and the associations wrong,
# which is the one failure a count check cannot catch, and it did: it filed
# aws-lgknx under no city at all, because Seoul is rowspanned from the entry
# above it.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
out="$root/simulator-gcp/compute_interconnect_remote_locations_vendored.json"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

base='https://docs.cloud.google.com/network-connectivity/docs/interconnect/how-to/cci'
for provider in aws azure oci alibaba; do
    curl -sSL --fail --max-time 60 -o "$work/$provider.html" "$base/$provider/choose-locations"
done

WORK="$work" BASE="$base" OUT="$out" python3 - <<'PY'
import datetime, json, os, re, sys

sys.path.insert(0, os.path.join(os.environ.get('ROOT', '.'), 'scripts', 'lib'))
sys.path.insert(0, os.path.join(os.path.dirname(os.environ['OUT']), '..', 'scripts', 'lib'))
import html_table_grid

work = os.environ['WORK']
locations = []
sources = {}

for provider in ('aws', 'azure', 'oci', 'alibaba'):
    html = open(os.path.join(work, f'{provider}.html'), encoding='utf-8', errors='replace').read()
    sources[provider] = f"{os.environ['BASE']}/{provider}/choose-locations"
    name_pattern = re.compile(rf'^{provider}-[a-z0-9-]+$')
    found = 0
    for grid in html_table_grid.tables(html):
        if not grid:
            continue
        header = [cell.lower() for cell in grid[0]]
        if 'remote location' not in header or 'metropolitan area' not in header:
            continue
        # Read the columns by their heading rather than by position, so a
        # column added or reordered upstream fails here instead of silently
        # shifting every field by one.
        metro_at = header.index('metropolitan area')
        connects_at = header.index('google cloud locations')
        remote_at = header.index('remote location')
        for row in grid[1:]:
            if len(row) != len(grid[0]):
                sys.exit(f'{provider}: a row has {len(row)} cells and the header has '
                         f'{len(grid[0])} — the grid did not square up')
            name = row[remote_at]
            if not name_pattern.match(name):
                continue
            record = {'name': name, 'city': row[metro_at]}
            facility = row[remote_at + 1]
            if facility and facility != name:
                record['facilityProviderFacilityId'] = facility
            permitted = re.findall(r'[a-z]{3}-zone\d-\d+', row[connects_at])
            if permitted:
                record['permittedConnections'] = [
                    {'interconnectLocation': location} for location in permitted]
            if not record['city']:
                sys.exit(f'{provider}: {name} has no metropolitan area; the rowspan '
                         f'carry-forward is not working and associations cannot be trusted')
            locations.append(record)
            found += 1
    if not found:
        sys.exit(f'{provider}: no remote locations found — the page layout changed')

names = [location['name'] for location in locations]
if len(names) != len(set(names)):
    duplicates = sorted({n for n in names if names.count(n) > 1})
    sys.exit(f'duplicate remote locations: {duplicates}')

locations.sort(key=lambda location: location['name'])
payload = {
    'sources': sources,
    'retrieved': datetime.date.today().isoformat(),
    'note': ('Every field here is stated by the source pages. The continent, the '
             'street address, the facility provider, the remote service, the LAG '
             'sizes and the LACP support are not, and are left absent rather than '
             'inferred.'),
    'remoteLocations': locations,
}
with open(os.environ['OUT'], 'w') as handle:
    json.dump(payload, handle, indent=1, sort_keys=False)
    handle.write('\n')
print(f'{len(locations)} remote locations -> {os.environ["OUT"]}')
PY
