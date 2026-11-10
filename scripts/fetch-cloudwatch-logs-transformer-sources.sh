#!/usr/bin/env bash
# Vendor what the Amazon CloudWatch Logs transformer processors draw on beyond
# the CloudWatch Logs guide, then regenerate the simulator's tables from it:
#
# - parseToOCSF: the OCSF mapping dossiers Amazon Security Lake publishes in
#   ocsf/examples (mappings/markdown/AWS/v1.1.0 and v1.5.0: CloudTrail,
#   Route 53 Resolver, VPC Flow Logs, EKS audit logs and AWS WAF, with their raw
#   and OCSF samples), and the OCSF 1.1.0 and 1.5.0 schemas whose classes,
#   enumerations and attribute types those dossiers name.
# - dateTimeConverter: java.time.format.DateTimeFormatter, whose pattern page
#   the guide links, as Java SE 17 ships it. Java SE 17 carries CLDR 39 for
#   month, day, quarter, era and AM/PM names, time zone names, metazones and
#   week data; the JDK's own time zone names (the COMPAT provider) fill the
#   names CLDR lacks; and the JDK sources record how the runtime reads both.
#
# A tag or release branch never moves, so its rows pin `commit<sha>`, which the
# freshness check skips; ocsf/examples rows pin the commit that last touched
# the directory, so the check reports a changed dossier.
#
# Usage: scripts/fetch-cloudwatch-logs-transformer-sources.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/specs/cloud-api/aws"
SOURCES="$DEST/SOURCES.md"
row() { bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" aws "$@"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
tarball() { # <dir> <out> <member>...
  local dir="$1" out="$2"
  shift 2
  tar -C "$dir" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf - "$@" | gzip -9 -n >"$out"
}

git clone --quiet --filter=blob:none --no-checkout --single-branch https://github.com/ocsf/examples "$work/examples"
for v in v1.1.0 v1.5.0; do
  dir="mappings/markdown/AWS/$v"
  sha="$(git -C "$work/examples" log -1 --format=%H -- "$dir")"
  mkdir -p "$work/ex-$v"
  git -C "$work/examples" archive "$sha" "$dir" | tar -C "$work/ex-$v" -xf -
  (cd "$work/ex-$v" && find "$dir" -type f ! -path '*/Security Hub/*' ! -name '*.png' | sort) >"$work/ex-$v.list"
  tarball "$work/ex-$v" "$DEST/ocsf-examples-aws-$v.tar.gz" -T "$work/ex-$v.list"
  row "ocsf-examples-aws-$v.tar.gz" ocsf/examples "$dir" Apache-2.0 "$sha"
done

for ref in v1.1.0 v1.5.0; do
  git clone --quiet --depth 1 --branch "$ref" https://github.com/ocsf/ocsf-schema "$work/schema-$ref"
  sha="$(git -C "$work/schema-$ref" rev-parse HEAD)"
  version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$work/schema-$ref/version.json")"
  if [ "v$version" != "$ref" ]; then
    echo "ocsf-schema $ref declares version $version" >&2
    exit 1
  fi
  for f in dictionary.json categories.json; do
    gzip -9 -n -c "$work/schema-$ref/$f" >"$DEST/ocsf-schema-$ref-$f.gz"
    row "ocsf-schema-$ref-$f.gz" "ocsf/ocsf-schema@$ref" "$f" Apache-2.0 "commit$sha"
  done
  for d in events objects profiles includes; do
    [ -d "$work/schema-$ref/$d" ] || continue
    tarball "$work/schema-$ref" "$DEST/ocsf-schema-$ref-$d.tar.gz" "$d"
    row "ocsf-schema-$ref-$d.tar.gz" "ocsf/ocsf-schema@$ref" "$d" Apache-2.0 "commit$sha"
  done
done

CLDR_TAG=39.0.0
cldr_sha="$(git ls-remote https://github.com/unicode-org/cldr-json "refs/tags/$CLDR_TAG^{}" "refs/tags/$CLDR_TAG" | sort -k2 | tail -1 | cut -f1)"
cldr() { curl -sSfL --retry 3 "https://raw.githubusercontent.com/unicode-org/cldr-json/$cldr_sha/cldr-json/$1" -o "$2"; }
mkdir -p "$work/cldr"
cldr cldr-core/availableLocales.json "$work/cldr/availableLocales.json"
python3 -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["availableLocales"]["full"]))' "$work/cldr/availableLocales.json" >"$work/cldr/locales"
while read -r loc; do
  mkdir -p "$work/cldr/main/$loc"
  cldr "cldr-dates-full/main/$loc/ca-gregorian.json" "$work/cldr/main/$loc/ca-gregorian.json"
  cldr "cldr-dates-full/main/$loc/timeZoneNames.json" "$work/cldr/main/$loc/timeZoneNames.json"
done <"$work/cldr/locales"
for f in ca-gregorian timeZoneNames; do
  (cd "$work/cldr" && find main -name "$f.json" | sort) >"$work/cldr/$f.list"
  name="cldr-$CLDR_TAG-dates-full-$(echo "$f" | tr '[:upper:]' '[:lower:]').tar.gz"
  tarball "$work/cldr" "$DEST/$name" -T "$work/cldr/$f.list"
  row "$name" "unicode-org/cldr-json@$CLDR_TAG" cldr-json/cldr-dates-full/main License-Unicode "commit$cldr_sha"
done
gzip -9 -n -c "$work/cldr/availableLocales.json" >"$DEST/cldr-$CLDR_TAG-available-locales.json.gz"
row "cldr-$CLDR_TAG-available-locales.json.gz" "unicode-org/cldr-json@$CLDR_TAG" cldr-json/cldr-core/availableLocales.json License-Unicode "commit$cldr_sha"
for f in metaZones:meta-zones weekData:week-data parentLocales:parent-locales dayPeriods:day-periods; do
  name="cldr-$CLDR_TAG-${f#*:}.json.gz"
  f="${f%%:*}"
  cldr "cldr-core/supplemental/$f.json" "$work/cldr/$f.json"
  gzip -9 -n -c "$work/cldr/$f.json" >"$DEST/$name"
  row "$name" "unicode-org/cldr-json@$CLDR_TAG" "cldr-json/cldr-core/supplemental/$f.json" License-Unicode "commit$cldr_sha"
done

cldr cldr-bcp47/bcp47/timezone.json "$work/cldr/timezone.json"
gzip -9 -n -c "$work/cldr/timezone.json" >"$DEST/cldr-$CLDR_TAG-bcp47-timezone.json.gz"
row "cldr-$CLDR_TAG-bcp47-timezone.json.gz" "unicode-org/cldr-json@$CLDR_TAG" cldr-json/cldr-bcp47/bcp47/timezone.json License-Unicode "commit$cldr_sha"

# cldr-json 39 carries no BCP 47 data; CLDR's own release tag carries the
# time zone aliases the JDK's CLDR converter reads.
cldr_xml_sha="$(git ls-remote https://github.com/unicode-org/cldr 'refs/tags/release-39^{}' | cut -f1)"
curl -sSfL --retry 3 "https://raw.githubusercontent.com/unicode-org/cldr/$cldr_xml_sha/common/bcp47/timezone.xml" | gzip -9 -n >"$DEST/cldr-39-bcp47-timezone.xml.gz"
row cldr-39-bcp47-timezone.xml.gz unicode-org/cldr@release-39 common/bcp47/timezone.xml License-Unicode "commit$cldr_xml_sha"

JDK_TAG=jdk-17-ga
jdk_sha="$(git ls-remote https://github.com/openjdk/jdk17u "refs/tags/$JDK_TAG^{}" | cut -f1)"
for path in \
  src/java.base/share/classes/java/time/format/DateTimeFormatterBuilder.java \
  src/java.base/share/classes/java/time/format/DateTimeTextProvider.java \
  src/java.base/share/classes/java/time/format/Parsed.java \
  src/java.base/share/classes/java/time/temporal/WeekFields.java \
  src/java.base/share/classes/sun/util/cldr/CLDRCalendarDataProviderImpl.java \
  src/java.base/share/classes/sun/util/cldr/CLDRTimeZoneNameProviderImpl.java \
  src/java.base/share/classes/sun/util/locale/provider/CalendarNameProviderImpl.java \
  make/jdk/src/classes/build/tools/cldrconverter/LDMLParseHandler.java \
  make/jdk/src/classes/build/tools/cldrconverter/MetaZonesParseHandler.java \
  src/java.base/share/classes/sun/util/resources/TimeZoneNames.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_de.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_es.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_fr.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_hi.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_it.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_ja.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_ko.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_pt_BR.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_sv.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_zh_CN.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_zh_TW.java \
  src/jdk.localedata/share/classes/sun/util/resources/ext/TimeZoneNames_zh_HK.java; do
  base="$(basename "$path" .java)"
  name="jdk17-$base.java.gz"
  curl -sSfL --retry 3 "https://raw.githubusercontent.com/openjdk/jdk17u/$jdk_sha/$path" | gzip -9 -n >"$DEST/$name"
  row "$name" "openjdk/jdk17u@$JDK_TAG" "$path" GPL-2.0-with-classpath-exception "commit$jdk_sha"
done

cd "$ROOT"
go run scripts/gen-cloudwatch-logs-ocsf.go
go run scripts/gen-cloudwatch-logs-cldr.go
