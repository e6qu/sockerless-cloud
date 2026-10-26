package azure_sdk_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The diagnostics side of a web app's SCM site: the zip API, the dump, the
// container log files, the process API, and the log stream of a LogFiles
// subdirectory.
//
//	GET+PUT /api/zip/{path}
//	GET /api/dump
//	GET /api/logs/docker
//	GET /api/logs/docker/zip
//	GET /api/processes
//	GET /api/processes/{id}
//	GET /api/processes/{id}/dump
//	GET /logstream/{path}

func kuduZipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err, "the body is not a zip")
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		rc.Close()
		require.NoError(t, err)
		out[f.Name] = string(content)
	}
	return out
}

// kuduOpenLogStream returns the stream's lines after the welcome line, and a
// cancel.
func kuduOpenLogStream(t *testing.T, scm, path string, auth func(*http.Request)) (*bufio.Scanner, context.CancelFunc) {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, scm+path, nil)
	require.NoError(t, err)
	auth(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	lines := bufio.NewScanner(resp.Body)
	require.True(t, lines.Scan(), "the stream opens with its welcome line")
	assert.Contains(t, lines.Text(), "Welcome, you are now connected to log-streaming service.")
	return lines, cancel
}

func kuduAwaitStreamLine(t *testing.T, lines *bufio.Scanner, match func(string) bool) string {
	t.Helper()
	for lines.Scan() {
		if match(lines.Text()) {
			return lines.Text()
		}
	}
	t.Fatalf("the stream ended before the awaited line: %v", lines.Err())
	return ""
}

func TestSDK_WebApps_KuduZipDumpAndProcesses(t *testing.T) {
	t.Parallel()
	rg, name := "sdk-kudu-diag-rg", "sdk-kudu-diag-app"
	client := createStackWebApp(t, rg, name, "DOCKER|"+servingImageName, nil)
	scm, auth := kuduSCM(t, client, rg, name)
	withHeader := func(k, v string) func(*http.Request) {
		return func(r *http.Request) { auth(r); r.Header.Set(k, v) }
	}

	// The zip API extracts an uploaded zip over a directory and downloads a
	// directory as a zip; files are not its business.
	bundle := makeJobsZip(t, map[string]string{"a.txt": "A", "sub/b.txt": "B"})
	resp, body := kuduRequest(t, http.MethodPut, scm+"/api/zip/site/wwwroot/bundle/", bundle, "application/zip", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/vfs/site/wwwroot/bundle/sub/b.txt", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Equal(t, "B", string(body))
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/zip/site/wwwroot/bundle/", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `filename="bundle.zip"`)
	assert.Equal(t, map[string]string{"a.txt": "A", "sub/b.txt": "B"}, kuduZipEntries(t, body))
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/zip/site/wwwroot/bundle/?fileName=named.zip", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `filename="named.zip"`)
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/zip/site/wwwroot/bundle/a.txt", nil, "", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the zip API serves directories, not files")
	resp, _ = kuduRequest(t, http.MethodPut, scm+"/api/zip/site/wwwroot/bundle/a.txt", bundle, "application/zip", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The container's output lands in the day's docker log under LogFiles, which
	// the log stream follows and the dump and container log APIs serve.
	stream, cancelStream := kuduOpenLogStream(t, scm, "/logstream", auth)
	status, body := azureSiteRequest(t, name, http.MethodGet, "/dump-me", "")
	require.Equal(t, http.StatusOK, status, "%s", body)
	kuduAwaitStreamLine(t, stream, func(line string) bool { return strings.HasSuffix(line, " GET /dump-me") })
	cancelStream()

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/logs/docker", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var containerLogs []struct {
		MachineName string `json:"machineName"`
		Size        int64  `json:"size"`
		Href        string `json:"href"`
		Path        string `json:"path"`
	}
	require.NoError(t, json.Unmarshal(body, &containerLogs), "%s", body)
	require.Len(t, containerLogs, 1, "one docker log for the running instance")
	assert.Regexp(t, `^[0-9a-f]{12}$`, containerLogs[0].MachineName, "the machine name is the instance's hostname")
	dockerLog := regexp.MustCompile(`^\d{4}_\d{2}_\d{2}_` + containerLogs[0].MachineName + `_docker\.log$`)
	logName := containerLogs[0].Href[strings.LastIndex(containerLogs[0].Href, "/")+1:]
	assert.Regexp(t, dockerLog, logName)
	assert.Equal(t, scm+"/api/vfs/LogFiles/"+logName, containerLogs[0].Href)
	assert.True(t, strings.HasSuffix(containerLogs[0].Path, "/LogFiles/"+logName), containerLogs[0].Path)
	assert.Positive(t, containerLogs[0].Size)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/vfs/LogFiles/"+logName, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), " GET /dump-me\n", "the docker log file holds the container's lines")

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/dump", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Regexp(t, `filename="dump-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}\.zip"`, resp.Header.Get("Content-Disposition"))
	dump := kuduZipEntries(t, body)
	require.Contains(t, dump, "LogFiles/"+logName, "the dump carries LogFiles: %v", dump)
	assert.Contains(t, dump["LogFiles/"+logName], " GET /dump-me\n")
	resp, body = kuduRequest(t, http.MethodGet, scm+"/dump", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the spelling az webapp log download fetches")
	assert.Contains(t, kuduZipEntries(t, body), "LogFiles/"+logName)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/logs/docker/zip", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Equal(t, []string{logName}, zipNames(kuduZipEntries(t, body)))

	// A LogFiles subdirectory's stream follows its files, keeping the lines the
	// filter names.
	stream, cancelStream = kuduOpenLogStream(t, scm, "/logstream/custom?filter=needle", auth)
	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/vfs/LogFiles/custom/app.log", []byte("first needle\nsecond\n"), "", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "%s", body)
	assert.Equal(t, "first needle", kuduAwaitStreamLine(t, stream, func(line string) bool { return !strings.Contains(line, "  ") }))
	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/vfs/LogFiles/custom/app.log", []byte("first needle\nsecond\nthird needle\n"), "", withHeader("If-Match", "*"))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "%s", body)
	assert.Equal(t, "third needle", kuduAwaitStreamLine(t, stream, func(line string) bool { return !strings.Contains(line, "  ") }),
		"only what the file gained streams, and only the lines the filter names")
	cancelStream()

	// The process API reads the site container's process table.
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/processes", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var processes []struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Href     string `json:"href"`
		Minidump string `json:"minidump"`
	}
	require.NoError(t, json.Unmarshal(body, &processes), "%s", body)
	require.NotEmpty(t, processes, "a running container has processes")
	first := processes[0]
	assert.Positive(t, first.ID)
	assert.NotEmpty(t, first.Name)
	assert.Equal(t, scm+"/api/processes/"+itoa(first.ID), first.Href)
	assert.Equal(t, first.Href+"/dump", first.Minidump)
	resp, body = kuduRequest(t, http.MethodGet, first.Href, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var one struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &one), "%s", body)
	assert.Equal(t, first.ID, one.ID)
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/processes/999999", nil, "", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The dump is the process's memory image, an ELF core; a host that does not
	// let the simulator read another process's memory says so.
	resp, body = kuduRequest(t, http.MethodGet, first.Minidump, nil, "", auth)
	if resp.StatusCode == http.StatusOK {
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
		core, err := elf.NewFile(bytes.NewReader(body))
		require.NoError(t, err, "the dump is not an ELF image a debugger opens")
		assert.Equal(t, elf.ET_CORE, core.Type)
		resp, body = kuduRequest(t, http.MethodGet, first.Minidump+"?format=zip", nil, "", auth)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
		assert.Equal(t, []string{"minidump.dmp"}, zipNames(kuduZipEntries(t, body)))
	} else {
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode, "%s", body)
		assert.Contains(t, strings.ToLower(string(body)), "read", "%s", body)
	}
}

func zipNames(entries map[string]string) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	return names
}
