package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_kudu_logstream.go serves Kudu's log stream on a site's SCM host: GET
// /logstream (and /api/logstream) holds the response open and writes, after
// a welcome line, each line appended to a .txt, .log or .htm file under the
// site's /home/LogFiles — the docker log among them — as it is appended;
// /logstream/{path} narrows that to the files under LogFiles/{path}, and a
// `filter` query keeps the lines containing it. Kudu on Linux watches the
// files by polling them (kudu-naive-file-system-watcher.cs), and so does
// this. While nothing is logged it writes a line each minute saying so, and
// it ends the stream once SCM_LOGSTREAM_TIMEOUT passes.

// kuduLogStreamInterval is the cadence Kudu's Linux watcher reads the log
// files at.
const kuduLogStreamInterval = 1500 * time.Millisecond

// kuduLogFileExtensions are the files the log stream follows.
var kuduLogFileExtensions = map[string]bool{".txt": true, ".log": true, ".htm": true}

// kuduLogStreamTime is the timestamp Kudu prefixes its own stream lines with.
func kuduLogStreamTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05")
}

// kuduLogStreamPath is the LogFiles-relative directory a log stream request
// addresses, and whether the request is one.
func kuduLogStreamPath(urlPath string) (string, bool) {
	lower := strings.ToLower(strings.TrimSuffix(urlPath, "/"))
	for _, prefix := range []string{"/api/logstream", "/logstream"} {
		if lower == prefix {
			return "", true
		}
		if strings.HasPrefix(lower, prefix+"/") {
			return strings.TrimPrefix(path.Clean("/"+urlPath[len(prefix):]), "/"), true
		}
	}
	return "", false
}

// kuduLogFiles are the log files under dir, with their sizes.
func kuduLogFiles(root *os.Root, dir string) map[string]int64 {
	sizes := map[string]int64{}
	_ = fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !kuduLogFileExtensions[strings.ToLower(path.Ext(p))] {
			return nil
		}
		if info, err := d.Info(); err == nil {
			sizes[p] = info.Size()
		}
		return nil
	})
	return sizes
}

func kuduLogStream(w http.ResponseWriter, r *http.Request, site *Site, rel string) {
	sim.DeclareOpenEndedWait(r.Context())
	home, err := kuduOpenHome(site)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = root.Close() }()
	dir := path.Join("LogFiles", rel)
	if err := root.MkdirAll(dir, 0o777); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filter := kuduQuery(r, "filter")
	limit := kuduSettingSeconds(site, "SCM_LOGSTREAM_TIMEOUT")
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	write := func(line string) bool {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(fmt.Sprintf("%s  Welcome, you are now connected to log-streaming service. The default timeout is 2 hours. Change the timeout with the App Setting SCM_LOGSTREAM_TIMEOUT (in seconds). ",
		kuduLogStreamTime(time.Now()))) {
		return
	}
	// The stream carries what is appended from here on; what the files held
	// at connection is not replayed.
	offsets := kuduLogFiles(root, dir)
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	poll := time.NewTicker(kuduLogStreamInterval)
	defer poll.Stop()
	lastLine := time.Now()
	quietMinutes := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			lines := kuduNewLogLines(root, dir, offsets, filter)
			for _, line := range lines {
				if !write(line) {
					return
				}
			}
			if len(lines) > 0 {
				lastLine, quietMinutes = time.Now(), 0
				continue
			}
			if minutes := int(time.Since(lastLine) / time.Minute); minutes > quietMinutes {
				quietMinutes = minutes
				if !write(fmt.Sprintf("%s  No new trace in the past %d min(s).", kuduLogStreamTime(time.Now()), quietMinutes)) {
					return
				}
			}
		case now := <-deadline.C:
			write(fmt.Sprintf("%s  Stream terminated due to timeout %d min(s).", kuduLogStreamTime(now), int(limit/time.Minute)))
			return
		}
	}
}

// kuduNewLogLines reads what the log files under dir gained since offsets
// recorded them, advancing offsets, and returns the complete lines that pass
// filter. A file that shrank is read from its start again, as Kudu does.
func kuduNewLogLines(root *os.Root, dir string, offsets map[string]int64, filter string) []string {
	sizes := kuduLogFiles(root, dir)
	names := make([]string, 0, len(sizes))
	for name := range sizes {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		size := sizes[name]
		offset, seen := offsets[name]
		if !seen {
			offset = 0
		}
		if size < offset {
			offset = 0
		}
		if size == offset {
			offsets[name] = offset
			continue
		}
		f, err := root.Open(name)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			continue
		}
		reader := bufio.NewReader(io.LimitReader(f, size-offset))
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				// A partial last line waits for its newline.
				break
			}
			offset += int64(len(line))
			line = strings.TrimRight(line, "\r\n")
			if filter == "" || strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
				out = append(out, line)
			}
		}
		_ = f.Close()
		offsets[name] = offset
	}
	return out
}
