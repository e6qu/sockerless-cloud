package main

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// RDSEngineLogHour holds the lines a DB instance's engine wrote during one UTC
// hour, in the order the container runtime dated them.
type RDSEngineLogHour struct {
	DbiResourceId string
	Hour          time.Time
	Lines         []RDSEngineLogLine
}

type RDSEngineLogLine struct {
	Written time.Time
	Stream  string
	Text    string
}

var rdsEngineLogs sim.Store[RDSEngineLogHour]

const (
	// rdsPostgresLogRetention is rds.log_retention_period's default of 4320
	// minutes.
	rdsPostgresLogRetention = 4320 * time.Minute
	// rdsMySQLErrorLogFlush is how often RDS for MySQL and MariaDB move
	// error/mysql-error.log into error/mysql-error-running.log, which it
	// rotates hourly into one of 24 numbered files.
	rdsMySQLErrorLogFlush = 5 * time.Minute
	// rdsLogPortionLimit caps a DownloadDBLogFilePortion response at 1 MB.
	rdsLogPortionLimit = 1 << 20
	// rdsLogPortionDefaultLines is what a DownloadDBLogFilePortion naming
	// neither Marker nor NumberOfLines returns at most.
	rdsLogPortionDefaultLines = 10000
)

func rdsEngineLogPrefix(resourceID string) string { return resourceID + "/" }

func rdsEngineLogKey(resourceID string, hour time.Time) string {
	return rdsEngineLogPrefix(resourceID) + hour.UTC().Format("2006010215")
}

// rdsEngineLogSink records an engine container's output under the instance's
// resource ID, so the log files outlive the container across a stop, a reboot
// and a simulator restart. An adopted container replays its output from the
// start; each stream is in order, so a line no later than the last one the
// stream recorded is one the sink already holds.
type rdsEngineLogSink struct {
	resourceID string
	mu         sync.Mutex
	last       map[string]time.Time
}

func newRDSEngineLogSink(resourceID string) *rdsEngineLogSink {
	return &rdsEngineLogSink{resourceID: resourceID}
}

func (s *rdsEngineLogSink) WriteLog(line sim.LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]time.Time{}
		for _, hour := range rdsEngineLogs.ListPrefix(rdsEngineLogPrefix(s.resourceID)) {
			for _, recorded := range hour.Item.Lines {
				if recorded.Written.After(s.last[recorded.Stream]) {
					s.last[recorded.Stream] = recorded.Written
				}
			}
		}
	}
	written := line.Timestamp.UTC()
	if !written.After(s.last[line.Stream]) {
		return
	}
	s.last[line.Stream] = written
	hour := written.Truncate(time.Hour)
	entry := RDSEngineLogLine{Written: written, Stream: line.Stream, Text: line.Text}
	rdsEngineLogs.Upsert(rdsEngineLogKey(s.resourceID, hour), func(h *RDSEngineLogHour) {
		h.DbiResourceId, h.Hour = s.resourceID, hour
		at := sort.Search(len(h.Lines), func(i int) bool { return h.Lines[i].Written.After(written) })
		h.Lines = slices.Insert(h.Lines, at, entry)
	})
}

func rdsDeleteEngineLogs(resourceID string) {
	for _, hour := range rdsEngineLogs.ListPrefix(rdsEngineLogPrefix(resourceID)) {
		rdsEngineLogs.Delete(hour.ID)
	}
}

type rdsLogFile struct {
	Name        string
	LastWritten time.Time
	Data        []byte
}

func rdsAppendLogLine(data []byte, line RDSEngineLogLine) []byte {
	data = append(data, line.Text...)
	return append(data, '\n')
}

// rdsEngineLogExpired reports whether an hour of engine output has left every
// log file the engine family keeps.
func rdsEngineLogExpired(family dbengine.Family, hour RDSEngineLogHour, now time.Time) bool {
	if family == dbengine.Postgres {
		return len(hour.Lines) == 0 || !hour.Lines[len(hour.Lines)-1].Written.After(now.Add(-rdsPostgresLogRetention))
	}
	return hour.Hour.Before(now.Truncate(time.Hour).Add(-24 * time.Hour))
}

// rdsLogFilesAt lays an instance's engine output out as the log files Amazon
// RDS lists for the engine family at now, sorted by name.
func rdsLogFilesAt(family dbengine.Family, hours []RDSEngineLogHour, now time.Time) []rdsLogFile {
	now = now.UTC()
	var live []RDSEngineLogHour
	for _, hour := range hours {
		if !rdsEngineLogExpired(family, hour, now) {
			live = append(live, hour)
		}
	}
	if len(live) == 0 {
		return nil
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Hour.Before(live[j].Hour) })
	var files []rdsLogFile
	if family == dbengine.Postgres {
		for _, hour := range live {
			file := rdsLogFile{Name: "error/postgresql.log." + hour.Hour.UTC().Format("2006-01-02-15")}
			for _, line := range hour.Lines {
				file.Data = rdsAppendLogLine(file.Data, line)
			}
			file.LastWritten = hour.Lines[len(hour.Lines)-1].Written
			files = append(files, file)
		}
		return files
	}

	flushed := now.Truncate(rdsMySQLErrorLogFlush)
	currentHour := now.Truncate(time.Hour)
	errorLog := rdsLogFile{Name: "error/mysql-error.log", LastWritten: flushed}
	running := rdsLogFile{Name: "error/mysql-error-running.log", LastWritten: currentHour}
	rotated := map[int]*rdsLogFile{}
	for _, hour := range live {
		for _, line := range hour.Lines {
			flushedAt := line.Written.Truncate(rdsMySQLErrorLogFlush).Add(rdsMySQLErrorLogFlush)
			switch {
			case !line.Written.Before(flushed):
				errorLog.Data = rdsAppendLogLine(errorLog.Data, line)
				errorLog.LastWritten = line.Written
			case !line.Written.Before(currentHour):
				running.Data = rdsAppendLogLine(running.Data, line)
				running.LastWritten = flushedAt
			default:
				index := hour.Hour.Add(time.Hour).UTC().Hour()
				file, ok := rotated[index]
				if !ok {
					file = &rdsLogFile{Name: "error/mysql-error-running.log." + strconv.Itoa(index)}
					rotated[index] = file
				}
				file.Data = rdsAppendLogLine(file.Data, line)
				file.LastWritten = flushedAt
			}
		}
	}
	files = append(files, errorLog, running)
	for _, file := range rotated {
		files = append(files, *file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files
}

// rdsInstanceLogFiles reads an instance's log files now, dropping the engine
// output no log file keeps any longer.
func rdsInstanceLogFiles(inst RDSInstance) []rdsLogFile {
	engine, ok := rdsEngine(inst.Engine, inst.EngineVersion)
	if !ok || rdsIsAurora(inst.Engine) {
		return nil
	}
	now := time.Now()
	var hours []RDSEngineLogHour
	for _, hour := range rdsEngineLogs.ListPrefix(rdsEngineLogPrefix(inst.DbiResourceId)) {
		if rdsEngineLogExpired(engine.Family, hour.Item, now) {
			rdsEngineLogs.Delete(hour.ID)
			continue
		}
		hours = append(hours, hour.Item)
	}
	return rdsLogFilesAt(engine.Family, hours, now)
}

func handleRDSDescribeLogFiles(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var filters []func(rdsLogFile) bool
	if contains := r.FormValue("FilenameContains"); contains != "" {
		filters = append(filters, func(f rdsLogFile) bool { return strings.Contains(f.Name, contains) })
	}
	for _, numeric := range []struct {
		field string
		keep  func(rdsLogFile, int64) bool
	}{
		{"FileLastWritten", func(f rdsLogFile, since int64) bool { return f.LastWritten.UnixMilli() >= since }},
		{"FileSize", func(f rdsLogFile, size int64) bool { return int64(len(f.Data)) > size }},
	} {
		raw := r.FormValue(numeric.field)
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			rdsErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid value %q for %s", raw, numeric.field), http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		keep := numeric.keep
		filters = append(filters, func(f rdsLogFile) bool { return keep(f, value) })
	}
	maxRecords := 0
	if raw := r.FormValue("MaxRecords"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			rdsErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid value %q for MaxRecords", raw), http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		maxRecords = parsed
	}
	marker := r.FormValue("Marker")

	var matched []rdsLogFile
	for _, file := range rdsInstanceLogFiles(inst) {
		if file.Name > marker && !slices.ContainsFunc(filters, func(keep func(rdsLogFile) bool) bool { return !keep(file) }) {
			matched = append(matched, file)
		}
	}
	next := ""
	if maxRecords > 0 && len(matched) > maxRecords {
		matched = matched[:maxRecords]
		next = matched[maxRecords-1].Name
	}
	var b strings.Builder
	b.WriteString("<DescribeDBLogFiles>")
	for _, file := range matched {
		b.WriteString("<DescribeDBLogFilesDetails>")
		fmt.Fprintf(&b, "<LogFileName>%s</LogFileName>", xmlEscape(file.Name))
		fmt.Fprintf(&b, "<LastWritten>%d</LastWritten>", file.LastWritten.UnixMilli())
		fmt.Fprintf(&b, "<Size>%d</Size>", len(file.Data))
		b.WriteString("</DescribeDBLogFilesDetails>")
	}
	b.WriteString("</DescribeDBLogFiles>")
	if next != "" {
		fmt.Fprintf(&b, "<Marker>%s</Marker>", xmlEscape(next))
	}
	rdsXMLResponse(w, "DescribeDBLogFiles", b.String(), sim.RequestID(r.Context()))
}

// rdsLogFilePortion cuts the portion DownloadDBLogFilePortion returns from a
// log file. Without a marker it returns the file's most recent lines and an
// end-of-file marker; from a marker — a byte offset, "0" being the start — it
// returns the lines that follow and whether more remain.
func rdsLogFilePortion(data []byte, marker string, numberOfLines int) (portion []byte, next int, pending bool, err error) {
	if marker == "" {
		lines := numberOfLines
		if lines <= 0 {
			lines = rdsLogPortionDefaultLines
		}
		start := len(data)
		for taken := 0; taken < lines && start > 0; taken++ {
			start = bytes.LastIndexByte(data[:start-1], '\n') + 1
		}
		if len(data)-start > rdsLogPortionLimit {
			start = len(data) - rdsLogPortionLimit
		}
		return data[start:], len(data), false, nil
	}
	offset, err := strconv.Atoi(marker)
	if err != nil || offset < 0 {
		return nil, 0, false, fmt.Errorf("invalid Marker %q", marker)
	}
	offset = min(offset, len(data))
	end := len(data)
	if numberOfLines > 0 {
		end = offset
		for taken := 0; taken < numberOfLines && end < len(data); taken++ {
			newline := bytes.IndexByte(data[end:], '\n')
			if newline < 0 {
				end = len(data)
				break
			}
			end += newline + 1
		}
	}
	end = min(end, offset+rdsLogPortionLimit)
	return data[offset:end], end, end < len(data), nil
}

func handleRDSDownloadLogFilePortion(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	name := r.FormValue("LogFileName")
	if name == "" {
		rdsErrorXML(w, "MissingParameter", "LogFileName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	numberOfLines := 0
	if raw := r.FormValue("NumberOfLines"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			rdsErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid value %q for NumberOfLines", raw), http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		numberOfLines = parsed
	}
	files := rdsInstanceLogFiles(inst)
	at := slices.IndexFunc(files, func(f rdsLogFile) bool { return f.Name == name })
	if at < 0 {
		rdsErrorXML(w, "DBLogFileNotFoundFault", fmt.Sprintf("DBLog File: %s, is not found on the DB instance", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	portion, next, pending, err := rdsLogFilePortion(files[at].Data, r.FormValue("Marker"), numberOfLines)
	if err != nil {
		rdsErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<LogFileData>%s</LogFileData>", xmlEscape(string(portion)))
	fmt.Fprintf(&b, "<Marker>%d</Marker>", next)
	fmt.Fprintf(&b, "<AdditionalDataPending>%t</AdditionalDataPending>", pending)
	rdsXMLResponse(w, "DownloadDBLogFilePortion", b.String(), sim.RequestID(r.Context()))
}
