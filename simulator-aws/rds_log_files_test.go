package main

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

func rdsTestLogLine(written string, text string) RDSEngineLogLine {
	at, err := time.Parse(time.RFC3339, written)
	if err != nil {
		panic(err)
	}
	return RDSEngineLogLine{Written: at, Stream: "stderr", Text: text}
}

func rdsTestLogHours(lines ...RDSEngineLogLine) []RDSEngineLogHour {
	byHour := map[time.Time]*RDSEngineLogHour{}
	var hours []RDSEngineLogHour
	var order []time.Time
	for _, line := range lines {
		hour := line.Written.Truncate(time.Hour)
		if _, ok := byHour[hour]; !ok {
			byHour[hour] = &RDSEngineLogHour{Hour: hour}
			order = append(order, hour)
		}
		byHour[hour].Lines = append(byHour[hour].Lines, line)
	}
	for _, hour := range order {
		hours = append(hours, *byHour[hour])
	}
	return hours
}

func TestRDSPostgreSQLLogFilesRotateHourly(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-01T12:30:00Z")
	files := rdsLogFilesAt(dbengine.Postgres, rdsTestLogHours(
		rdsTestLogLine("2026-09-28T12:29:00Z", "expired"),
		rdsTestLogLine("2026-10-01T11:05:00Z", "LOG:  database system is ready to accept connections"),
		rdsTestLogLine("2026-10-01T11:59:59Z", "ERROR:  relation \"missing\" does not exist"),
		rdsTestLogLine("2026-10-01T12:10:00Z", "LOG:  checkpoint starting: time"),
	), now)
	if len(files) != 2 {
		t.Fatalf("files = %+v, want two hourly files", files)
	}
	first, second := files[0], files[1]
	if first.Name != "error/postgresql.log.2026-10-01-11" || second.Name != "error/postgresql.log.2026-10-01-12" {
		t.Fatalf("names = %q, %q", first.Name, second.Name)
	}
	wantFirst := "LOG:  database system is ready to accept connections\nERROR:  relation \"missing\" does not exist\n"
	if string(first.Data) != wantFirst {
		t.Fatalf("first file = %q", first.Data)
	}
	if want, _ := time.Parse(time.RFC3339, "2026-10-01T11:59:59Z"); !first.LastWritten.Equal(want) {
		t.Fatalf("first LastWritten = %s", first.LastWritten)
	}
}

// The times are those of the AWS CLI's describe-db-log-files example: the
// error log flushed at 18:20, the running log rotated at 18:00, and running
// logs .23, .0 and .18 last flushed at 22:50, 23:45 and 17:15.
func TestRDSMySQLErrorLogsFlushAndRotateAsTheCLIExampleShows(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2018-07-31T18:22:00Z")
	files := rdsLogFilesAt(dbengine.MySQL, rdsTestLogHours(
		rdsTestLogLine("2018-07-30T17:30:00Z", "rotated out a day ago"),
		rdsTestLogLine("2018-07-30T22:47:00Z", "line a"),
		rdsTestLogLine("2018-07-30T23:44:00Z", "line b"),
		rdsTestLogLine("2018-07-31T17:12:00Z", "line c"),
	), now)
	got := map[string]rdsLogFile{}
	var names []string
	for _, file := range files {
		got[file.Name] = file
		names = append(names, file.Name)
	}
	want := []string{
		"error/mysql-error-running.log",
		"error/mysql-error-running.log.0",
		"error/mysql-error-running.log.18",
		"error/mysql-error-running.log.23",
		"error/mysql-error.log",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for name, lastWritten := range map[string]int64{
		"error/mysql-error-running.log":    1533060000000,
		"error/mysql-error-running.log.0":  1532994300000,
		"error/mysql-error-running.log.18": 1533057300000,
		"error/mysql-error-running.log.23": 1532991000000,
		"error/mysql-error.log":            1533061200000,
	} {
		if got[name].LastWritten.UnixMilli() != lastWritten {
			t.Errorf("%s LastWritten = %d, want %d", name, got[name].LastWritten.UnixMilli(), lastWritten)
		}
	}
	if len(got["error/mysql-error.log"].Data) != 0 || len(got["error/mysql-error-running.log"].Data) != 0 {
		t.Fatalf("current files hold data: %+v", files)
	}
	if string(got["error/mysql-error-running.log.18"].Data) != "line c\n" {
		t.Fatalf(".18 = %q", got["error/mysql-error-running.log.18"].Data)
	}

	later, _ := time.Parse(time.RFC3339, "2018-07-31T18:27:30Z")
	files = rdsLogFilesAt(dbengine.MySQL, rdsTestLogHours(
		rdsTestLogLine("2018-07-31T18:21:00Z", "flushed"),
		rdsTestLogLine("2018-07-31T18:26:00Z", "pending"),
	), later)
	for _, file := range files {
		switch file.Name {
		case "error/mysql-error.log":
			if string(file.Data) != "pending\n" {
				t.Errorf("error log = %q", file.Data)
			}
		case "error/mysql-error-running.log":
			if string(file.Data) != "flushed\n" || file.LastWritten.Format(time.RFC3339) != "2018-07-31T18:25:00Z" {
				t.Errorf("running log = %q at %s", file.Data, file.LastWritten)
			}
		}
	}
}

func TestRDSEngineWithoutOutputHasNoLogFiles(t *testing.T) {
	if files := rdsLogFilesAt(dbengine.MySQL, nil, time.Now()); len(files) != 0 {
		t.Fatalf("files = %+v", files)
	}
	if files := rdsLogFilesAt(dbengine.Postgres, nil, time.Now()); len(files) != 0 {
		t.Fatalf("files = %+v", files)
	}
}

func TestRDSLogFilePortion(t *testing.T) {
	data := []byte("one\ntwo\nthree\nfour\nfive\n")

	tail, next, pending, err := rdsLogFilePortion(data, "", 0)
	if err != nil || string(tail) != string(data) || next != len(data) || pending {
		t.Fatalf("whole tail = %q %d %t %v", tail, next, pending, err)
	}
	tail, next, pending, _ = rdsLogFilePortion(data, "", 2)
	if string(tail) != "four\nfive\n" || next != len(data) || pending {
		t.Fatalf("two-line tail = %q %d %t", tail, next, pending)
	}

	var assembled []byte
	marker, rounds := "0", 0
	for {
		portion, next, pending, err := rdsLogFilePortion(data, marker, 2)
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, portion...)
		rounds++
		if !pending {
			break
		}
		marker = strconv.Itoa(next)
	}
	if !bytes.Equal(assembled, data) || rounds != 3 {
		t.Fatalf("paged = %q in %d rounds", assembled, rounds)
	}

	rest, next, pending, _ := rdsLogFilePortion(data, "8", 0)
	if string(rest) != "three\nfour\nfive\n" || next != len(data) || pending {
		t.Fatalf("from marker = %q %d %t", rest, next, pending)
	}

	big := bytes.Repeat([]byte(strings.Repeat("x", 1023)+"\n"), 1500)
	portion, next, pending, _ := rdsLogFilePortion(big, "0", 0)
	if len(portion) != rdsLogPortionLimit || next != rdsLogPortionLimit || !pending {
		t.Fatalf("capped portion = %d bytes, next %d, pending %t", len(portion), next, pending)
	}
	tail, _, _, _ = rdsLogFilePortion(big, "", 0)
	if len(tail) != rdsLogPortionLimit || !bytes.HasSuffix(big, tail) {
		t.Fatalf("capped tail = %d bytes", len(tail))
	}

	if _, _, _, err := rdsLogFilePortion(data, "start", 0); err == nil {
		t.Fatal("a marker that is no offset was accepted")
	}
}

// An adopted engine container replays its output from the start; the sink
// keeps each line once, and dates lines from both streams into one order.
func TestRDSEngineLogSinkRecordsEachLineOnce(t *testing.T) {
	rdsEngineLogs = sim.MakeStore[RDSEngineLogHour](nil, "rds_engine_logs")
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	lines := []sim.LogLine{
		{Stream: "stdout", Text: "initdb", Timestamp: base.Add(2 * time.Second)},
		{Stream: "stderr", Text: "starting", Timestamp: base.Add(1 * time.Second)},
		{Stream: "stderr", Text: "ready", Timestamp: base.Add(3 * time.Second)},
	}
	sink := newRDSEngineLogSink("db-RESOURCE")
	for _, line := range lines {
		sink.WriteLog(line)
	}
	replay := newRDSEngineLogSink("db-RESOURCE")
	for _, line := range lines {
		replay.WriteLog(line)
	}
	replay.WriteLog(sim.LogLine{Stream: "stderr", Text: "after adoption", Timestamp: base.Add(4 * time.Second)})

	stored := rdsEngineLogs.ListPrefix(rdsEngineLogPrefix("db-RESOURCE"))
	if len(stored) != 1 {
		t.Fatalf("hours = %d", len(stored))
	}
	var texts []string
	for _, line := range stored[0].Item.Lines {
		texts = append(texts, line.Text)
	}
	if got := strings.Join(texts, ","); got != "starting,initdb,ready,after adoption" {
		t.Fatalf("lines = %s", got)
	}
	rdsDeleteEngineLogs("db-RESOURCE")
	if rdsEngineLogs.Len() != 0 {
		t.Fatal("deleting the instance's logs left records")
	}
}

type rdsTestLogFilesResponse struct {
	Files []struct {
		LogFileName string `xml:"LogFileName"`
		LastWritten int64  `xml:"LastWritten"`
		Size        int64  `xml:"Size"`
	} `xml:"DescribeDBLogFilesResult>DescribeDBLogFiles>DescribeDBLogFilesDetails"`
	Marker string `xml:"DescribeDBLogFilesResult>Marker"`
}

func rdsLogCall(t *testing.T, handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestRDSDescribeDBLogFilesFiltersAndPages(t *testing.T) {
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsEngineLogs = sim.MakeStore[RDSEngineLogHour](nil, "rds_engine_logs")
	const id, resourceID = "log-files-db", "db-LOGFILES"
	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, DbiResourceId: resourceID, Engine: "postgres", DBInstanceStatus: "available"})

	empty := rdsLogCall(t, handleRDSDescribeLogFiles, url.Values{"DBInstanceIdentifier": {id}})
	var none rdsTestLogFilesResponse
	if err := xml.Unmarshal(empty.Body.Bytes(), &none); err != nil || len(none.Files) != 0 {
		t.Fatalf("an engine that never ran lists %s", empty.Body.String())
	}
	missing := rdsLogCall(t, handleRDSDownloadLogFilePortion, url.Values{"DBInstanceIdentifier": {id}, "LogFileName": {"error/postgresql.log"}})
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "<Code>DBLogFileNotFoundFault</Code>") {
		t.Fatalf("missing file = %d %s", missing.Code, missing.Body.String())
	}

	sink := newRDSEngineLogSink(resourceID)
	now := time.Now().UTC()
	for elapsed, text := range []string{"two hours ago", "an hour ago, a longer line", "current hour"} {
		sink.WriteLog(sim.LogLine{Stream: "stderr", Text: text, Timestamp: now.Add(time.Duration(elapsed-2) * time.Hour)})
	}

	var all rdsTestLogFilesResponse
	if err := xml.Unmarshal(rdsLogCall(t, handleRDSDescribeLogFiles, url.Values{"DBInstanceIdentifier": {id}}).Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Files) != 3 || all.Marker != "" {
		t.Fatalf("files = %+v marker %q", all.Files, all.Marker)
	}

	var paged []string
	marker := ""
	for {
		var page rdsTestLogFilesResponse
		form := url.Values{"DBInstanceIdentifier": {id}, "MaxRecords": {"2"}}
		if marker != "" {
			form.Set("Marker", marker)
		}
		if err := xml.Unmarshal(rdsLogCall(t, handleRDSDescribeLogFiles, form).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, file := range page.Files {
			paged = append(paged, file.LogFileName)
		}
		if page.Marker == "" {
			break
		}
		marker = page.Marker
	}
	if len(paged) != 3 || paged[0] != all.Files[0].LogFileName || paged[2] != all.Files[2].LogFileName {
		t.Fatalf("paged = %v", paged)
	}

	var bySize rdsTestLogFilesResponse
	_ = xml.Unmarshal(rdsLogCall(t, handleRDSDescribeLogFiles, url.Values{
		"DBInstanceIdentifier": {id}, "FileSize": {"20"},
	}).Body.Bytes(), &bySize)
	if len(bySize.Files) != 1 || bySize.Files[0].Size != int64(len("an hour ago, a longer line\n")) {
		t.Fatalf("FileSize filter = %+v", bySize.Files)
	}
	var recent rdsTestLogFilesResponse
	_ = xml.Unmarshal(rdsLogCall(t, handleRDSDescribeLogFiles, url.Values{
		"DBInstanceIdentifier": {id}, "FileLastWritten": {strconv.Itoa(int(now.Add(-90 * time.Minute).UnixMilli()))},
	}).Body.Bytes(), &recent)
	if len(recent.Files) != 2 {
		t.Fatalf("FileLastWritten filter = %+v", recent.Files)
	}
	var named rdsTestLogFilesResponse
	_ = xml.Unmarshal(rdsLogCall(t, handleRDSDescribeLogFiles, url.Values{
		"DBInstanceIdentifier": {id}, "FilenameContains": {now.Format("2006-01-02-15")},
	}).Body.Bytes(), &named)
	if len(named.Files) != 1 || named.Files[0].LogFileName != "error/postgresql.log."+now.Format("2006-01-02-15") {
		t.Fatalf("FilenameContains filter = %+v", named.Files)
	}

	download := rdsLogCall(t, handleRDSDownloadLogFilePortion, url.Values{
		"DBInstanceIdentifier": {id}, "LogFileName": {named.Files[0].LogFileName}, "Marker": {"0"},
	})
	var portion struct {
		LogFileData           string `xml:"DownloadDBLogFilePortionResult>LogFileData"`
		Marker                string `xml:"DownloadDBLogFilePortionResult>Marker"`
		AdditionalDataPending bool   `xml:"DownloadDBLogFilePortionResult>AdditionalDataPending"`
	}
	if err := xml.Unmarshal(download.Body.Bytes(), &portion); err != nil {
		t.Fatal(err)
	}
	if portion.LogFileData != "current hour\n" || portion.AdditionalDataPending || portion.Marker != strconv.Itoa(len("current hour\n")) {
		t.Fatalf("download = %+v", portion)
	}
}
