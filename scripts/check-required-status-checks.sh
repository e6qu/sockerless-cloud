#!/usr/bin/env bash
# check-required-status-checks.sh — guard against required-check drift (BUG-2633).
#
# Every context in .github/required-status-checks.txt must be emittable by some
# job in .github/workflows/*.yml. A matrix job rename or removal that leaves a
# required check unable to report fails here — at pull-request time — instead of
# silently stalling the merge queue on a context that can never turn green.
#
# Usage:
#   scripts/check-required-status-checks.sh
#       Default: enumerate every check name the workflows can emit (rendering
#       each job's `name:` over its matrix) and fail if any required context is
#       not among them. No network access.
#   scripts/check-required-status-checks.sh --verify-branch-protection
#       Additionally read `main`'s live required-status-checks via `gh api` and
#       fail if the manifest and branch protection disagree. Requires GitHub
#       admin credentials; fails loudly (never skips) if they are unavailable.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
manifest="$root/.github/required-status-checks.txt"
workflow_dir="$root/.github/workflows"

if [[ ! -f "$manifest" ]]; then
	echo "check-required-status-checks: missing manifest $manifest" >&2
	exit 1
fi

# read_manifest prints the required context names, dropping comments and blanks.
read_manifest() {
	grep -vE '^[[:space:]]*(#|$)' "$manifest"
}

# emittable_names prints every check-context name any workflow job can emit,
# rendering `name:` templates over the job's matrix. Python does the parsing —
# matrix expansion is not something bash reads reliably.
emittable_names() {
	python3 - "$workflow_dir" <<'PY'
import os, re, sys

workflow_dir = sys.argv[1]
name_var = re.compile(r'\$\{\{\s*matrix\.(\w+)\s*\}\}')


def job_blocks(lines):
    """Yield (job_id, [lines]) for each job under a top-level `jobs:` mapping."""
    in_jobs = False
    job_id = None
    block = []
    for line in lines:
        if re.match(r'^jobs:\s*$', line):
            in_jobs = True
            continue
        if not in_jobs:
            continue
        # A column-0 key ends the jobs mapping.
        if line and not line[0].isspace() and not line.lstrip().startswith('#'):
            break
        m = re.match(r'^  (\S[^:]*):\s*$', line)
        if m:
            if job_id is not None:
                yield job_id, block
            job_id = m.group(1)
            block = []
            continue
        if job_id is not None:
            block.append(line)
    if job_id is not None:
        yield job_id, block


def strip_val(v):
    return v.strip().strip('"').strip("'")


def matrix_values(block, var):
    """Collect the values a matrix variable takes, across the three forms used
    in these workflows: inline flow list (`var: [a, b]`), block list (`var:`
    then `- a`), and include maps (`- var: a`)."""
    vals = []
    in_matrix = False
    matrix_indent = -1
    block_list = False
    block_list_indent = -1
    for line in block:
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip())
        stripped = line.strip()
        if re.match(r'^matrix:\s*$', stripped):
            in_matrix = True
            matrix_indent = indent
            continue
        if not in_matrix:
            continue
        if indent <= matrix_indent:
            break  # matrix block ended
        m = re.match(rf'^{re.escape(var)}:\s*\[(.*)\]\s*$', stripped)
        if m:
            vals += [strip_val(x) for x in m.group(1).split(',') if x.strip()]
            block_list = False
            continue
        m = re.match(rf'^-\s*{re.escape(var)}:\s*(\S.*)$', stripped)
        if m:
            vals.append(strip_val(m.group(1)))
            block_list = False
            continue
        if re.match(rf'^{re.escape(var)}:\s*$', stripped):
            block_list = True
            block_list_indent = indent
            continue
        if block_list:
            m = re.match(r'^-\s*(\S.*)$', stripped)
            if m and indent > block_list_indent:
                vals.append(strip_val(m.group(1)))
                continue
            block_list = False
    return vals


def include_combos(block):
    """Yield one dict per `include:` entry, which GitHub runs as its own job.

    A matrix written entirely as includes cannot be read as a cross product:
    the combinations are exactly the entries, and a variable set on one entry
    says nothing about another. That is how a suite is split — two entries
    sharing a cloud and a suite, differing only in their shard — and reading it
    as a product would both invent jobs and, when a name refers to a variable
    only some entries carry, render nothing at all."""
    combos = []
    in_matrix = False
    matrix_indent = -1
    in_include = False
    include_indent = -1
    current = None
    for line in block:
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip())
        stripped = line.strip()
        if stripped.startswith('#'):
            continue
        if re.match(r'^matrix:\s*$', stripped):
            in_matrix, matrix_indent = True, indent
            continue
        if not in_matrix:
            continue
        if indent <= matrix_indent:
            break
        if re.match(r'^include:\s*$', stripped):
            in_include, include_indent = True, indent
            continue
        if not in_include:
            continue
        if indent <= include_indent:
            in_include = False
            continue
        m = re.match(r'^-\s*(\w+):\s*(.*)$', stripped)
        if m:
            if current is not None:
                combos.append(current)
            current = {m.group(1): strip_val(m.group(2))}
            continue
        m = re.match(r'^(\w+):\s*(.*)$', stripped)
        if m and current is not None:
            current[m.group(1)] = strip_val(m.group(2))
    if current is not None:
        combos.append(current)
    return combos


# A name that appends a variable only some combinations carry, written the one
# way GitHub Actions expresses it:
#   ${{ matrix.shard && format(' {0}', matrix.shard) || '' }}
conditional_var = re.compile(
    r"\$\{\{\s*matrix\.(\w+)\s*&&\s*format\(\s*'([^']*)'\s*,\s*matrix\.\1\s*\)\s*\|\|\s*''\s*\}\}")


def substitute(template, combo):
    def conditional(m):
        var, shape = m.group(1), m.group(2)
        value = combo.get(var, '')
        return shape.replace('{0}', value) if value else ''

    rendered = conditional_var.sub(conditional, template)
    for var, val in combo.items():
        rendered = re.sub(r'\$\{\{\s*matrix\.' + re.escape(var) + r'\s*\}\}', val, rendered)
    return rendered


def render(template, block):
    vars_used = set(name_var.findall(template)) | set(m[0] for m in conditional_var.findall(template))
    if not vars_used:
        return [template]
    combos = include_combos(block)
    if combos:
        # Entries that name every variable the template refers to are the jobs
        # this matrix runs.
        rendered = [substitute(template, c) for c in combos
                    if all(v in c or conditional_var.search(template) for v in vars_used)]
        if rendered:
            return rendered
    # Build the cartesian product over the referenced matrix variables.
    product = [{}]
    for var in vars_used:
        values = matrix_values(block, var)
        if not values:
            return []  # a referenced matrix variable with no values emits nothing
        product = [dict(c, **{var: v}) for c in product for v in values]
    return [substitute(template, c) for c in product]


emittable = set()
for fn in sorted(os.listdir(workflow_dir)):
    if not (fn.endswith('.yml') or fn.endswith('.yaml')):
        continue
    with open(os.path.join(workflow_dir, fn)) as fh:
        lines = fh.read().splitlines()
    for job_id, block in job_blocks(lines):
        template = None
        for line in block:
            m = re.match(r'^    name:\s*(\S.*)$', line)
            if m:
                template = strip_val(m.group(1))
                break
        if template is None:
            template = job_id  # GitHub uses the job id when no name is set
        for n in render(template, block):
            emittable.add(n)

for n in sorted(emittable):
    print(n)
PY
}

emittable="$(emittable_names)"

missing=()
while IFS= read -r ctx; do
	[[ -z "$ctx" ]] && continue
	if ! grep -Fxq "$ctx" <<<"$emittable"; then
		missing+=("$ctx")
	fi
done < <(read_manifest)

if ((${#missing[@]} > 0)); then
	echo "check-required-status-checks: required contexts no workflow can emit:" >&2
	for ctx in "${missing[@]}"; do
		echo "  - $ctx" >&2
	done
	echo "Update the workflow that should emit it and .github/required-status-checks.txt together," >&2
	echo "then update main's branch-protection required-status-checks to match." >&2
	exit 1
fi

if [[ "${1:-}" == "--verify-branch-protection" ]]; then
	if ! command -v gh >/dev/null 2>&1; then
		echo "check-required-status-checks: --verify-branch-protection needs the gh CLI" >&2
		exit 1
	fi
	origin="$(git -C "$root" remote get-url origin)"
	slug="$(sed -E 's#^.*github\.com[:/]+##; s#\.git$##' <<<"$origin")"
	if ! live="$(gh api "repos/$slug/branches/main/protection/required_status_checks" --jq '.contexts[]' 2>/dev/null)"; then
		echo "check-required-status-checks: could not read branch protection for $slug" >&2
		echo "(needs GitHub admin credentials — this mode is for maintainers, not the default gate)." >&2
		exit 1
	fi
	manifest_sorted="$(read_manifest | sort -u)"
	live_sorted="$(sort -u <<<"$live")"
	if ! diff <(printf '%s\n' "$manifest_sorted") <(printf '%s\n' "$live_sorted") >/dev/null; then
		echo "check-required-status-checks: manifest disagrees with main's branch protection:" >&2
		diff <(printf '%s\n' "$manifest_sorted") <(printf '%s\n' "$live_sorted") >&2 || true
		echo "('<' only in manifest, '>' only in branch protection). Reconcile them." >&2
		exit 1
	fi
	echo "check-required-status-checks: manifest matches main's branch protection."
fi
