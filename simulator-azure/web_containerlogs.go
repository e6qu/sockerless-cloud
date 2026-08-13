package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_containerlogs.go implements WebApps_GetWebSiteContainerLogs[Slot] and
// WebApps_GetContainerLogsZip[Slot]. The log text is the site container's
// real output: every line a site container writes flows through funcLogSink
// into the AppTraces rows of the Azure Monitor store (bounded at
// monitorMaxRetainedRows), and these endpoints render exactly those retained
// rows — timestamped, in arrival order — as the docker log file real App
// Service serves.

// webSiteContainerLogText assembles the retained container log lines for a
// site (AppRoleName is the site name funcLogSink stamps on every row).
func webSiteContainerLogText(siteName string) []byte {
	logMu.Lock()
	rows, _ := monitorLogs.Get("default:AppTraces")
	logMu.Unlock()
	var buf bytes.Buffer
	for _, row := range rows {
		if row["AppRoleName"] != siteName {
			continue
		}
		fmt.Fprintf(&buf, "%s %s\n", row["TimeGenerated"], row["Message"])
	}
	return buf.Bytes()
}

func registerWebContainerLogs(both func(string, string, http.HandlerFunc)) {
	// WebApps_GetWebSiteContainerLogs[Slot] — the raw docker log text;
	// 204 when the container has produced no retained output.
	both("POST", "/containerlogs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		logs := webSiteContainerLogText(site.Name)
		if len(logs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(logs)
	})

	// WebApps_GetContainerLogsZip[Slot] — the same log content packaged as
	// the ZIP archive real App Service serves (one docker log file inside).
	both("POST", "/containerlogs/zip/download", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		logs := webSiteContainerLogText(site.Name)
		if len(logs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		name := fmt.Sprintf("%s_%s_docker.log", time.Now().UTC().Format("2006_01_02"), site.Name)
		f, err := zw.Create(name)
		if err == nil {
			_, err = f.Write(logs)
		}
		if cerr := zw.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			sim.AzureError(w, "InternalServerError", "Failed to assemble the container log archive: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})
}
