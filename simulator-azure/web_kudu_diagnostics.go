package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// web_kudu_diagnostics.go serves the rest of Kudu's diagnostics surface on a
// site's SCM host, over the site's /home, the storage its containers mount:
// the zip API (/api/zip/{path}: a directory downloads as a zip and a zip
// uploaded to one extracts into it), /api/dump (the deployment and LogFiles
// directories zipped, which `az webapp log download` fetches at /dump),
// /api/logs/docker (the current container log files, and /zip for them
// zipped) and the process API (/api/processes, one process, and its dump),
// backed by the same engine reads Azure Resource Manager's process
// operations use. The sources are the vendored Kudu controllers
// (kudu-zip-controller.cs, kudu-diagnostics-controller.cs,
// kudu-process-controller.cs).

// webSiteMachineNames holds each site's current instance name, which its
// container sees as its hostname and its docker log file carries.
var webSiteMachineNames sync.Map

// webNewSiteMachineName names the instance a site's container is about to
// become, the way App Service names a worker: twelve lowercase hex digits.
func webNewSiteMachineName(siteName string) string {
	machine := sim.RandomHex(12)
	webSiteMachineNames.Store(siteName, machine)
	return machine
}

// webSiteMachineName is the instance name of the site's running container.
func webSiteMachineName(siteName string) (string, bool) {
	v, ok := webSiteMachineNames.Load(siteName)
	if !ok {
		return "", false
	}
	machine, ok := v.(string)
	return machine, ok
}

// webDockerLogMu serializes appends to the docker log files.
var webDockerLogMu sync.Mutex

// webAppendDockerLogFile writes one line of a site's container output to the
// day's docker log in the site's LogFiles directory,
// YYYY_MM_DD_<machine>_docker.log, where App Service keeps it.
func webAppendDockerLogFile(siteID, at, message string) {
	site, ok := webSiteRecord(siteID)
	if !ok {
		return
	}
	machine, ok := webSiteMachineName(site.Name)
	if !ok {
		return
	}
	stamp, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		stamp = time.Now()
	}
	home, err := ensureSiteHome(site.Name)
	if err != nil {
		logger := azureServer.Logger()
		logger.Error().Err(err).Str("site", site.Name).Msg("write the site's docker log file")
		return
	}
	webDockerLogMu.Lock()
	defer webDockerLogMu.Unlock()
	name := fmt.Sprintf("%s_%s_docker.log", stamp.UTC().Format("2006_01_02"), machine)
	f, err := os.OpenFile(path.Join(home, "LogFiles", name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		logger := azureServer.Logger()
		logger.Error().Err(err).Str("site", site.Name).Msg("open the site's docker log file")
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %s\n", at, message)
}

// kuduContainerLogName matches a container log file that has not been
// rolled: YYYY_MM_DD_<machinename>_(docker|app|platform|console).log.
var kuduContainerLogName = regexp.MustCompile(`^\d{4}_\d{2}_\d{2}_.*_(docker|app|platform|console)\.log$`)

// kuduCurrentContainerLogs lists the container log files of the latest day
// in the site's LogFiles directory.
func kuduCurrentContainerLogs(root *os.Root) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(root.FS(), "LogFiles")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var logs []fs.DirEntry
	latest := ""
	for _, e := range entries {
		if e.IsDir() || !kuduContainerLogName.MatchString(e.Name()) {
			continue
		}
		if day := e.Name()[:10]; day > latest {
			latest = day
		}
		logs = append(logs, e)
	}
	current := logs[:0]
	for _, e := range logs {
		if strings.HasPrefix(e.Name(), latest) {
			current = append(current, e)
		}
	}
	sort.Slice(current, func(i, j int) bool { return current[i].Name() < current[j].Name() })
	return current, nil
}

// kuduZipPath is the /home-relative path a zip API request addresses and
// whether it named a directory (a trailing slash).
func kuduZipPath(urlPath string) (rel string, dir bool, ok bool) {
	lower := strings.ToLower(urlPath)
	if !strings.HasPrefix(lower, "/api/zip/") && lower != "/api/zip" {
		return "", false, false
	}
	rest := urlPath[len("/api/zip"):]
	dir = rest == "" || strings.HasSuffix(rest, "/")
	rel = strings.TrimPrefix(path.Clean("/"+rest), "/")
	return rel, dir, true
}

// serveKuduZip is the zip API: GET on a directory answers its contents as a
// zip, PUT on a directory extracts the uploaded zip over it, and a file is
// not a thing the zip API serves.
func serveKuduZip(w http.ResponseWriter, r *http.Request, site *Site, rel string, dir bool) {
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
	name := rel
	if name == "" {
		name = "."
	}
	info, statErr := root.Stat(name)
	switch r.Method {
	case http.MethodGet:
		if statErr != nil {
			kuduWebAPIError(w, http.StatusNotFound, fmt.Sprintf("'%s' not found.", "/home/"+rel))
			return
		}
		if !info.IsDir() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !dir {
			u := *r.URL
			u.Path += "/"
			u.RawPath = ""
			w.Header().Set("Location", azureRequestScheme(r)+"://"+r.Host+u.RequestURI())
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		fileName := kuduQuery(r, "fileName")
		if fileName == "" {
			fileName = path.Base(path.Join(home, rel)) + ".zip"
		}
		var buf bytes.Buffer
		if err := kuduZipDirectory(&buf, root, name, ""); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		kuduWriteZip(w, fileName, buf.Bytes())
	case http.MethodPut:
		if statErr == nil && !info.IsDir() || statErr != nil && !dir {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if kuduVFSRefuseReadOnly(w, site, rel) {
			return
		}
		src, ok := kuduReadArtifact(w, r)
		if !ok {
			return
		}
		data := src.data
		if src.packageURI != "" {
			if data, err = webFetchPackage(src.packageURI); err != nil {
				kuduWebAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		var files []archive.File
		if err := archive.ReadZip(data, webSiteContentLimit, func(f archive.File) error {
			files = append(files, f)
			return nil
		}); err != nil {
			kuduWebAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		// The zip extracts over what the directory holds; nothing is removed.
		if _, err := webWriteSiteHome(site, "/home/"+rel, files, false); err != nil {
			kuduWebAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := kuduVFSCommit(site, rel)(); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		kuduMethod(w, r, http.MethodGet, http.MethodPut)
	}
}

// kuduZipDirectory adds the files under dir to zw, named relative to dir
// under prefix: a directory's own contents sit at the root of its zip.
func kuduZipDirectory(buf *bytes.Buffer, root *os.Root, dir, prefix string) error {
	zw := zip.NewWriter(buf)
	if err := kuduAddDirectory(zw, root, dir, prefix, nil); err != nil {
		return err
	}
	return zw.Close()
}

// kuduAddDirectory adds dir's files to zw under prefix, skipping the
// top-level entries skip rejects.
func kuduAddDirectory(zw *zip.Writer, root *os.Root, dir, prefix string, skip func(fs.DirEntry) bool) error {
	return fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel := strings.TrimPrefix(p, dir+"/")
		if dir == "." {
			rel = p
		}
		if skip != nil && !strings.Contains(rel, "/") && skip(d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		return kuduAddFile(zw, root, p, path.Join(prefix, rel))
	})
}

// kuduAddFile adds the file at p to zw as name, with its own modification
// time.
func kuduAddFile(zw *zip.Writer, root *os.Root, p, name string) error {
	info, err := root.Stat(p)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	dst, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(dst, f)
	return err
}

// kuduWriteZip answers with a zip attachment.
func kuduWriteZip(w http.ResponseWriter, fileName string, data []byte) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// kuduDump is GET /api/dump (and /dump): the site's deployments directory,
// its LogFiles directory without the W3SVC request traces, and the npm debug
// log in wwwroot, zipped as dump-MM-dd-HH-mm-ss.zip.
func kuduDump(w http.ResponseWriter, r *http.Request, site *Site) {
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
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if info, err := root.Stat("site/deployments"); err == nil && info.IsDir() {
		if err := kuduAddDirectory(zw, root, "site/deployments", "deployments", nil); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if info, err := root.Stat("LogFiles"); err == nil && info.IsDir() {
		notTraces := func(d fs.DirEntry) bool {
			return d.IsDir() && strings.HasPrefix(strings.ToUpper(d.Name()), "W3SVC")
		}
		if err := kuduAddDirectory(zw, root, "LogFiles", "LogFiles", notTraces); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if info, err := root.Stat("site/wwwroot/npm-debug.log"); err == nil && !info.IsDir() {
		if err := kuduAddFile(zw, root, "site/wwwroot/npm-debug.log", "npm-debug.log"); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := zw.Close(); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	kuduWriteZip(w, time.Now().UTC().Format("dump-01-02-15-04-05.zip"), buf.Bytes())
}

// kuduContainerLogs is GET /api/logs/docker: the current container log
// files, each with its machine name and VFS link; /api/logs/docker/zip
// answers the same files zipped.
func kuduContainerLogs(w http.ResponseWriter, r *http.Request, site *Site, zipped bool) {
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
	logs, err := kuduCurrentContainerLogs(root)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if zipped {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, e := range logs {
			if err := kuduAddFile(zw, root, "LogFiles/"+e.Name(), e.Name()); err != nil {
				kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if err := zw.Close(); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		kuduWriteZip(w, time.Now().UTC().Format("containerlogs-01-02-15-04-05.zip"), buf.Bytes())
		return
	}
	out := make([]map[string]any, 0, len(logs))
	for _, e := range logs {
		info, err := e.Info()
		if err != nil {
			continue
		}
		// YYYY_MM_DD_<machinename>_<kind>.log: the machine name follows the
		// eleven characters of the day.
		machine := e.Name()[11:]
		machine = machine[:strings.IndexByte(machine, '_')]
		out = append(out, map[string]any{
			"machineName": machine,
			"lastUpdated": info.ModTime().UTC().Format(time.RFC3339Nano),
			"size":        info.Size(),
			"href":        azureRequestScheme(r) + "://" + r.Host + "/api/vfs/LogFiles/" + e.Name(),
			"path":        path.Join(home, "LogFiles", e.Name()),
		})
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// serveKuduProcesses is the process API: GET /api/processes lists the site
// container's processes, GET /api/processes/{id} reads one, DELETE kills it,
// and GET /api/processes/{id}/dump reads its memory image, raw or zipped.
func serveKuduProcesses(w http.ResponseWriter, r *http.Request, site *Site, rest string) {
	instance, running := webSiteInstanceContainer(*site)
	var procs []webProcess
	if running {
		var err error
		if procs, err = webProcessTable(instance.ID()); err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	base := azureRequestScheme(r) + "://" + r.Host + "/api"
	readAt := time.Now()
	if rest == "" {
		if !kuduMethod(w, r, http.MethodGet) {
			return
		}
		out := make([]map[string]any, 0, len(procs))
		for _, proc := range procs {
			out = append(out, kuduProcessInfo(base, proc, procs, readAt))
		}
		sim.WriteJSON(w, http.StatusOK, out)
		return
	}
	idText, tail, _ := strings.Cut(rest, "/")
	pid, err := strconv.Atoi(idText)
	if err != nil {
		kuduWebAPIError(w, http.StatusBadRequest, "The process identifier must be a number.")
		return
	}
	var proc *webProcess
	for i := range procs {
		if procs[i].PID == pid {
			proc = &procs[i]
		}
	}
	if proc == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch strings.ToLower(tail) {
	case "":
		switch r.Method {
		case http.MethodGet:
			sim.WriteJSON(w, http.StatusOK, kuduProcessInfo(base, *proc, procs, readAt))
		case http.MethodDelete, http.MethodPost:
			if err := webKillContainerProcess(instance, pid); err != nil {
				kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			kuduMethod(w, r, http.MethodGet, http.MethodDelete, http.MethodPost)
		}
	case "dump":
		if !kuduMethod(w, r, http.MethodGet) {
			return
		}
		format := strings.ToLower(kuduQuery(r, "format"))
		if format != "" && format != "raw" && format != "zip" {
			kuduWebAPIError(w, http.StatusBadRequest, fmt.Sprintf("Dump format '%s' is not supported.", format))
			return
		}
		core, err := webReadProcess(r.Context(), instance.ID(), pid, "core")
		if err != nil {
			kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if format == "zip" {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			entry, err := zw.Create("minidump.dmp")
			if err == nil {
				_, err = entry.Write(core)
			}
			if closeErr := zw.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
			kuduWriteZip(w, "minidump.zip", buf.Bytes())
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="minidump.dmp"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(core)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(core)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// kuduProcessInfo is Kudu's ProcessInfo: the members Azure Resource Manager's
// ProcessInfo carries, which proxies it, with the process's own id and name
// and its links on the SCM host.
func kuduProcessInfo(base string, proc webProcess, procs []webProcess, readAt time.Time) map[string]any {
	doc := webProcessDoc(base, "", proc, procs, readAt)
	info, _ := doc["properties"].(map[string]any)
	info["id"] = proc.PID
	info["name"] = webProcessFileName(proc.CommandLine)
	info["minidump"] = webProcessResourceID(base, "", proc.PID) + "/dump"
	return info
}
