#!/usr/bin/env bash
# Regenerates simulator-gcp/compute_waf_expression_sets_vendored.json from
# Google's published Cloud Armor preconfigured-WAF-rules documentation.
#
# securityPolicies.listPreconfiguredExpressionSets answers with Google's own
# catalogue of WAF signatures, so it is vendored — as the colocation facilities
# beside it are, and Application Gateway's managed rule sets on the Azure slice.
#
# Two things about the source decide the shape of this script.
#
# The signatures live in per-version tables and the sets in a status table, and
# the two are joined by the identifier: `owasp-crs-v042200-id942100-sqli` names
# both the CRS release and the category, which is the set. Deriving the set
# names that way is only safe because it is checked — every set the status
# tables declare must come out of the derivation, and the script exits if one
# does not.
#
# The exceptions are the two vulnerability sets, and they are why this is not a
# pattern match. cve-canary's signatures are four OWASP Log4j ids plus two
# `google-mrs-` React ids, and json-sqli-canary's single signature is
# `owasp-crs-id942550-sqli` — no CRS-version segment at all, unlike every other
# id on the page. Composing that one by analogy would have produced
# `owasp-crs-v030001-id942550-sqli`, a string that appears in no repository
# anywhere; the real one appears in Google's own terraform-google-waap. It is
# read from the page, not built.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
source_url='https://docs.cloud.google.com/armor/docs/waf-rules'
out="$root/simulator-gcp/compute_waf_expression_sets_vendored.json"
page="$(mktemp)"
trap 'rm -f "$page"' EXIT

curl -sSL --fail --max-time 60 -o "$page" "$source_url"

ROOT="$root" SOURCE_URL="$source_url" OUT="$out" python3 - "$page" <<'PY'
import collections, datetime, json, os, re, sys

sys.path.insert(0, os.path.join(os.environ['ROOT'], 'scripts', 'lib'))
import html_table_grid

html = open(sys.argv[1], encoding='utf-8', errors='replace').read()
tables = html_table_grid.tables(html)

# The sets Google declares, and whether a stable one is in sync with its canary.
declared = {}
for grid in tables:
    header = [cell.lower() for cell in grid[0]] if grid else []
    if 'cloud armor rule name' not in header or 'current status' not in header:
        continue
    name_at, status_at = header.index('cloud armor rule name'), header.index('current status')
    for row in grid[1:]:
        name = row[name_at]
        if re.fullmatch(r'[a-z0-9-]+-(stable|canary)', name):
            declared[name] = row[status_at]

# The signatures, joined to their set by the identifier.
version_infix = {'042200': '-v422', '030301': '-v33', '030001': ''}
sets = collections.defaultdict(list)
for grid in tables:
    header = [cell.lower() for cell in grid[0]] if grid else []
    if 'signature id (rule id)' not in header or 'sensitivity level' not in header:
        continue
    id_at, sensitivity_at = header.index('signature id (rule id)'), header.index('sensitivity level')
    for row in grid[1:]:
        identifier, sensitivity = row[id_at], row[sensitivity_at]
        if not sensitivity.isdigit():
            continue
        versioned = re.fullmatch(r'owasp-crs-v(\d+)-id\d+-([a-z]+)', identifier)
        expression = {'id': identifier, 'sensitivity': int(sensitivity)}
        if versioned and versioned.group(2) != 'cve':
            base = versioned.group(2) + version_infix[versioned.group(1)]
            for suffix in ('-stable', '-canary'):
                sets[base + suffix].append(expression)
        elif identifier.startswith('google-mrs-') or (versioned and versioned.group(2) == 'cve'):
            sets['cve-canary'].append(expression)
        elif re.fullmatch(r'owasp-crs-id\d+-[a-z]+', identifier):
            # The unversioned form, which only the JSON SQLi set uses.
            sets['json-sqli-canary'].append(expression)
        elif identifier.startswith('owasp') or identifier.startswith('google-'):
            sys.exit(f'{identifier} matches no set; the identifier scheme changed')

missing = sorted(set(declared) - set(sets))
if missing:
    sys.exit(f'the page declares {missing} but no signature table maps to them')

expression_sets = []
for name in sorted(sets):
    # A set's aliases are the other names Google answers it under; the page
    # states none beyond the names themselves, so none are invented here.
    expression_sets.append({'id': name, 'expressions': sets[name]})

payload = {
    'source': os.environ['SOURCE_URL'],
    'retrieved': datetime.date.today().isoformat(),
    'note': ('Signatures and sensitivities are read from the per-version tables and '
             'joined to their set by the identifier, which names both the CRS release '
             'and the category. Every set the status tables declare is checked to come '
             'out of that derivation. The two vulnerability sets are read directly: '
             'json-sqli-canary uses an unversioned identifier that composing by analogy '
             'would get wrong.'),
    'expressionSets': expression_sets,
}
with open(os.environ['OUT'], 'w') as handle:
    json.dump(payload, handle, indent=1, sort_keys=False)
    handle.write('\n')
signatures = sum(len(entry['expressions']) for entry in expression_sets)
print(f'{len(expression_sets)} expression sets, {signatures} signature slots -> {os.environ["OUT"]}')
PY
