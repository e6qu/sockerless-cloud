package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cwdConvert(t *testing.T, converter map[string]any, value string) (string, error) {
	t.Helper()
	converter["source"], converter["target"] = "in", "out"
	b, err := json.Marshal([]map[string]any{{"parseJSON": map[string]any{}}, {"dateTimeConverter": converter}})
	require.NoError(t, err)
	tr, err := cwtCompileJSON(t, string(b))
	require.NoError(t, err, "%s", b)
	in, err := json.Marshal(map[string]string{"in": value})
	require.NoError(t, err)
	res := tr.Transform(string(in), nil)
	if res.Err != nil {
		return "", res.Err
	}
	var out map[string]string
	require.NoError(t, json.Unmarshal([]byte(res.Transformed), &out))
	return out["out"], nil
}

// Each case is what Java SE 17's DateTimeFormatter prints for the pattern,
// locale and zone, from the CLDR 39 data and JDK sources it ships.
func TestCWTransformerDateTimePatternLetters(t *testing.T) {
	cases := []struct {
		name, locale, zone, pattern, in, want string
	}{
		{"English zone name in standard time", "en", "America/New_York", "yyyy-MM-dd HH:mm z", "2023-01-15T12:00:00Z", "2023-01-15 07:00 EST"},
		{"English zone name in daylight time", "en", "America/New_York", "HH:mm zzzz", "2023-07-15T12:00:00Z", "08:00 Eastern Daylight Time"},
		{"COMPAT fills the short name CLDR lacks", "en", "America/Sao_Paulo", "HH:mm z", "2023-07-15T12:00:00Z", "09:00 BRT"},
		{"German metazone short name", "de", "Europe/Berlin", "HH:mm z", "2023-01-15T12:00:00Z", "13:00 MEZ"},
		{"German summer short name", "de", "Europe/Berlin", "HH:mm z", "2023-07-15T12:00:00Z", "14:00 MESZ"},
		{"German long generic name", "de", "Europe/Berlin", "vvvv", "2023-07-15T12:00:00Z", "Mitteleuropäische Zeit"},
		{"zone ID", "en", "Asia/Tokyo", "VV", "2023-07-15T12:00:00Z", "Asia/Tokyo"},
		{"offset zone prints its ID for z", "en", "+05:30", "z", "2023-07-15T12:00:00Z", "+05:30"},
		{"localized offset", "en", "Europe/Berlin", "O|OOOO", "2023-07-15T12:00:00Z", "GMT+2|GMT+02:00"},
		{"French localized offset uses UTC", "fr", "Europe/Paris", "O", "2023-07-15T12:00:00Z", "UTC+2"},
		{"French month and day names", "fr", "UTC", "EEEE d MMMM yyyy|EEE d MMM", "1998-12-05T10:00:00Z", "samedi 5 décembre 1998|sam. 5 déc."},
		{"Russian stand-alone month", "ru", "UTC", "LLLL|MMMM", "2023-01-15T00:00:00Z", "январь|января"},
		{"narrow names", "en", "UTC", "MMMMM EEEEE GGGGG", "2023-01-15T00:00:00Z", "J S A"},
		{"quarters", "en", "UTC", "Q QQ QQQ QQQQ qqqq", "2023-11-15T00:00:00Z", "4 04 Q4 4th quarter 4th quarter"},
		{"eras", "en", "UTC", "G GGGG", "2023-11-15T00:00:00Z", "AD Anno Domini"},
		{"US week fields", "en-US", "UTC", "YYYY-ww-e c W", "2023-01-01T00:00:00Z", "2023-01-1 1 1"},
		{"German week fields", "de-DE", "UTC", "YYYY-ww-e W", "2023-01-01T00:00:00Z", "2022-52-7 0"},
		{"a locale without a region weeks like the US", "de", "UTC", "YYYY-ww", "2023-01-01T00:00:00Z", "2023-01"},
		{"day periods", "en", "UTC", "h B|BBBB|BBBBB", "2023-01-15T15:00:00Z", "3 in the afternoon|in the afternoon|in the afternoon"},
		{"noon and midnight", "en", "UTC", "B", "2023-01-15T12:00:00Z", "noon"},
		{"clock hours and hours of AM/PM", "en", "UTC", "k K h H a", "2023-01-15T00:05:00Z", "24 0 12 0 AM"},
		{"fractions, milli-of-day, nano-of-day", "en", "UTC", "SSS n A N", "2023-01-15T00:00:01.500Z", "500 500000000 1500 1500000000"},
		{"Modified Julian Day and aligned day of week", "en", "UTC", "g F D DDD", "2023-01-15T00:00:00Z", "59959 1 15 015"},
		{"offsets", "en", "Asia/Kolkata", "X XX XXX x xx xxx Z ZZZZ ZZZZZ", "2023-01-15T00:00:00Z", "+0530 +0530 +05:30 +0530 +0530 +05:30 +0530 GMT+05:30 +05:30"},
		{"zero offsets", "en", "UTC", "X|x|xx|xxx|Z|ZZZZZ", "2023-01-15T00:00:00Z", "Z|+00|+0000|+00:00|+0000|Z"},
		{"years and padding", "en", "UTC", "u uu uuuu yyyyy ppd", "2023-01-05T00:00:00Z", "2023 23 2023 02023  5"},
		{"years before the common era", "en", "UTC", "y u G", "-0044-03-15T00:00:00Z", "45 -44 BC"},
		{"optional sections print", "en", "UTC", "yyyy[-MM[-dd]]", "2023-01-15T00:00:00Z", "2023-01-15"},
		{"root locale month names are US short names", "", "UTC", "MMMM MMM EEE", "2023-01-15T00:00:00Z", "Jan Jan Sun"},
		{"Chinese with a region resolves its script", "zh-TW", "UTC", "MMMM EEEE", "2023-01-15T00:00:00Z", "1月 星期日"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conv := map[string]any{"matchPatterns": []string{"uuuu-MM-dd'T'HH:mm:ss[.SSS]X"}, "targetFormat": c.pattern, "targetTimezone": c.zone}
			if c.locale != "" {
				conv["locale"] = c.locale
			}
			got, err := cwdConvert(t, conv, c.in)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestCWTransformerDateTimeParsing(t *testing.T) {
	cases := []struct {
		name, locale, source string
		patterns             []string
		in, want             string
	}{
		{"optional time", "en", "UTC", []string{"yyyy-MM-dd['T'HH:mm[:ss]]"}, "2023-05-01T10:30", "2023-05-01T10:30:00.000Z"},
		{"optional time absent", "en", "UTC", []string{"yyyy-MM-dd['T'HH:mm[:ss]]"}, "2023-05-01", "2023-05-01T00:00:00.000Z"},
		{"zone name", "en", "UTC", []string{"dd MMM yyyy HH:mm:ss z"}, "03 Aug 2023 12:34:56 EDT", "2023-08-03T16:34:56.000Z"},
		{"long zone name", "en", "UTC", []string{"dd MMM yyyy HH:mm:ss zzzz"}, "03 Aug 2023 12:34:56 Central European Summer Time", "2023-08-03T10:34:56.000Z"},
		{"COMPAT zone name", "en", "UTC", []string{"yyyy-MM-dd HH:mm z"}, "2023-08-03 12:00 BRT", "2023-08-03T15:00:00.000Z"},
		{"German zone name", "de", "UTC", []string{"yyyy-MM-dd HH:mm z"}, "2023-01-03 12:00 MEZ", "2023-01-03T11:00:00.000Z"},
		{"zone ID", "en", "UTC", []string{"yyyy-MM-dd HH:mm VV"}, "2023-01-03 12:00 Asia/Tokyo", "2023-01-03T03:00:00.000Z"},
		{"UTC-prefixed zone", "en", "UTC", []string{"yyyy-MM-dd HH:mm VV"}, "2023-01-03 12:00 UTC+02:00", "2023-01-03T10:00:00.000Z"},
		{"localized offset", "en", "UTC", []string{"yyyy-MM-dd HH:mm O"}, "2023-01-03 12:00 GMT+5", "2023-01-03T07:00:00.000Z"},
		{"adjacent values", "en", "UTC", []string{"yyyyMMddHHmmss"}, "20230103120000", "2023-01-03T12:00:00.000Z"},
		{"two-digit year", "en", "UTC", []string{"dd.MM.yy"}, "05.12.98", "2098-12-05T00:00:00.000Z"},
		{"day of year", "en", "UTC", []string{"yyyy DDD"}, "2024 366", "2024-12-31T00:00:00.000Z"},
		{"week-based date", "de-DE", "UTC", []string{"YYYY-'W'ww-e"}, "2023-W01-1", "2023-01-02T00:00:00.000Z"},
		{"week of month", "en-US", "UTC", []string{"yyyy-MM W e"}, "2023-01 2 1", "2023-01-08T00:00:00.000Z"},
		{"Modified Julian Day", "en", "UTC", []string{"g"}, "59959", "2023-01-15T00:00:00.000Z"},
		{"quarter cross-checks the date", "en", "UTC", []string{"yyyy-MM-dd QQQ"}, "2023-11-15 Q4", "2023-11-15T00:00:00.000Z"},
		{"hour of AM/PM", "en", "UTC", []string{"yyyy-MM-dd h:mm a"}, "2023-01-15 3:05 PM", "2023-01-15T15:05:00.000Z"},
		{"day period with hour of AM/PM", "en", "UTC", []string{"yyyy-MM-dd h B"}, "2023-01-15 3 in the afternoon", "2023-01-15T15:00:00.000Z"},
		{"day period alone takes its midpoint", "en", "UTC", []string{"yyyy-MM-dd B"}, "2023-01-15 in the morning", "2023-01-15T09:00:00.000Z"},
		{"AM/PM alone", "en", "UTC", []string{"yyyy-MM-dd a"}, "2023-01-15 PM", "2023-01-15T18:00:00.000Z"},
		{"24:00 is the next day's midnight", "en", "UTC", []string{"yyyy-MM-dd HH:mm"}, "2023-01-15 24:00", "2023-01-16T00:00:00.000Z"},
		{"February 30 resolves to its last day", "en", "UTC", []string{"yyyy-MM-dd"}, "2023-02-30", "2023-02-28T00:00:00.000Z"},
		{"era with year of era prints as the year of era", "en", "UTC", []string{"yyyy-MM-dd G"}, "0044-03-15 BC", "0044-03-15T00:00:00.000Z"},
		{"French names", "fr", "Europe/Paris", []string{"EEEE d MMMM yyyy HH:mm"}, "samedi 5 décembre 1998 11:00", "1998-12-05T10:00:00.000Z"},
		{"fraction", "en", "UTC", []string{"HH:mm:ss.SSSSSS yyyy-MM-dd"}, "10:00:00.123456 2023-01-15", "2023-01-15T10:00:00.123Z"},
		{"spring-forward gap moves later", "en", "America/New_York", []string{"yyyy-MM-dd HH:mm"}, "2023-03-12 02:30", "2023-03-12T07:30:00.000Z"},
		{"fall-back overlap keeps the earlier offset", "en", "America/New_York", []string{"yyyy-MM-dd HH:mm"}, "2023-11-05 01:30", "2023-11-05T05:30:00.000Z"},
		{"pad", "en", "UTC", []string{"yyyy-MM-ppd"}, "2023-01- 5", "2023-01-05T00:00:00.000Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conv := map[string]any{"matchPatterns": c.patterns, "sourceTimezone": c.source}
			if c.locale != "" {
				conv["locale"] = c.locale
			}
			got, err := cwdConvert(t, conv, c.in)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestCWTransformerDateTimeParseFailures(t *testing.T) {
	cases := []struct {
		name, pattern, in, want string
	}{
		{"unparsed text", "yyyy-MM-dd", "2023-01-15x", "Text '2023-01-15x' could not be parsed, unparsed text found at index 10"},
		{"wrong text", "yyyy-MM-dd", "2023-xx-15", "Text '2023-xx-15' could not be parsed at index 5"},
		{"case-sensitive names", "dd MMM yyyy", "15 jan 2023", "Text '15 jan 2023' could not be parsed at index 3"},
		{"month out of range", "yyyy-MM-dd", "2023-13-01", "Text '2023-13-01' could not be parsed: Invalid value for MonthOfYear (valid values 1 - 12): 13"},
		{"day of week conflicts with date", "EEE yyyy-MM-dd", "Mon 2023-01-15", "Conflict found: Field DayOfWeek 7 differs from DayOfWeek 1 derived from 2023-01-15"},
		{"AM/PM conflicts with the hour", "yyyy-MM-dd HH a", "2023-01-15 10 PM", "Conflict found: Field AmPmOfDay 0 differs from AmPmOfDay 1 derived from 10:00"},
		{"no date", "HH:mm", "10:00", "the text has no date"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := cwdConvert(t, map[string]any{"matchPatterns": []string{c.pattern}, "locale": "en"}, c.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// Every locale CLDR 39 publishes resolves to month, day, era and day
// period names, and every zone the simulator resolves has names in it.
func TestCWDLocaleTables(t *testing.T) {
	for id := range cwdLocaleParent {
		l, err := cwdLocaleOf(id)
		require.NoError(t, err, id)
		for s := cwdFull; s <= cwdNarrowStandalone; s++ {
			for i, n := range l.months(s) {
				assert.NotEmpty(t, n, "%s month %d style %d", id, i+1, s)
			}
			for i, n := range l.days(s) {
				assert.NotEmpty(t, n, "%s day %d style %d", id, i, s)
			}
		}
		assert.NotEmpty(t, l.periods(cwdFull)[0], "%s am", id)
		assert.NotEmpty(t, l.eras(cwdShort)[1], "%s era", id)
		assert.NotEmpty(t, l.formats[1], "%s gmtFormat", id)
	}
	en, err := cwdLocaleOf("en")
	require.NoError(t, err)
	for _, id := range cwdZoneIDs() {
		for i, n := range en.zoneNames(id) {
			assert.NotEmpty(t, n, "%s name %d", id, i)
		}
	}
	assert.Equal(t, "Asia/Calcutta", cwdCanonicalID("Asia/Kolkata"))
	assert.Equal(t, "America/New_York", en.preferredZone("America/Detroit"))
}
