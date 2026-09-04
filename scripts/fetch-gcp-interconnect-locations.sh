#!/usr/bin/env bash
# Regenerates simulator-gcp/compute_interconnect_locations_vendored.json from
# Google's published colocation-facility documentation.
#
# The catalogue of facilities Cloud Interconnect runs out of is Google's to
# publish and cannot be derived from anything the simulator holds, so it is
# vendored — the same way simulator-azure/network_appgateway_waf_rule_sets.go
# vendors Application Gateway's managed WAF rule sets, and for the same reason.
#
# The page is fetched with curl and parsed here rather than read by anything
# that summarises, because a summary of a catalogue is a catalogue with invented
# entries in it. The parser verifies its own alignment: the table declares five
# columns, every group of five cells must put a location name in the second, and
# the number of names recovered must equal the number of names anywhere on the
# page. It fails rather than emitting a short catalogue — Google's own markup
# omits the <tr> on one row (Cape Town), which a row-oriented parser drops
# silently and this one does not.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
source_url='https://docs.cloud.google.com/network-connectivity/docs/interconnect/concepts/choosing-colocation-facilities'
out="$root/simulator-gcp/compute_interconnect_locations_vendored.json"
page="$(mktemp)"
trap 'rm -f "$page"' EXIT

curl -sSL --fail --max-time 60 -o "$page" "$source_url"

SOURCE_URL="$source_url" OUT="$out" python3 - "$page" <<'PY'
import json, os, re, sys, datetime

html = open(sys.argv[1], encoding='utf-8', errors='replace').read()
every_name = set(re.findall(r'\b([a-z]{3}-zone\d-\d+)\b', html))

records = []
for table in re.findall(r'<table[^>]*>(.*?)</table>', html, re.S):
    cells = re.findall(r'<td[^>]*>(.*?)</td>', table, re.S)
    if not cells:
        continue
    if len(cells) % 5:
        sys.exit(f'table has {len(cells)} cells, which is not a whole number of '
                 f'five-column rows — the page layout changed')
    for i in range(0, len(cells), 5):
        metro, name_cell, facility, region_cell, _link = cells[i:i + 5]
        name = re.sub(r'&\w+;', '', re.sub(r'<[^>]+>', '', name_cell)).strip()
        if not re.fullmatch(r'[a-z]{3}-zone\d-\d+', name):
            sys.exit(f'cell {i+1} of a five-column row is {name!r}, not a location '
                     f'name — the columns are not where the header says')
        record = {
            'name': name,
            'city': re.sub(r'\s+', ' ', re.sub(r'<[^>]+>', '', metro)).strip(),
            'description': re.sub(r'\s+', ' ', re.sub(r'<[^>]+>', '', facility)).strip(),
            'availabilityZone': re.search(r'-(zone\d)-', name).group(1),
        }
        peeringdb = re.search(r'peeringdb\.com/fac/(\d+)', facility)
        if peeringdb:
            record['peeringdbFacilityId'] = peeringdb.group(1)
        region = re.sub(r'<[^>]+>', '', region_cell).strip()
        if region:
            record['regionInfos'] = [{'region': region}]
        records.append(record)

names = {r['name'] for r in records}
if names != every_name:
    sys.exit(f'recovered {len(names)} location names but the page carries '
             f'{len(every_name)}; missing {sorted(every_name - names)}')

records.sort(key=lambda r: r['name'])
payload = {
    'source': os.environ['SOURCE_URL'],
    'retrieved': datetime.date.today().isoformat(),
    'note': ('Every field here is stated by the source. The street address, the '
             'facility provider, the continent and the available link types are '
             'not: the page gives a geographic grouping and a link-speed column '
             'whose mapping onto the enums Compute Engine declares is a '
             'judgement, and a field the source does not state is left absent '
             'rather than inferred.'),
    'locations': records,
}
with open(os.environ['OUT'], 'w') as handle:
    json.dump(payload, handle, indent=1, sort_keys=False)
    handle.write('\n')
print(f'{len(records)} colocation facilities -> {os.environ["OUT"]}')
PY
