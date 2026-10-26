package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WebApps_GetWebSiteContainerLogs and GetContainerLogsZip render the site's
// retained docker log — every line its container wrote, through funcLogSink,
// bounded at webSiteDockerLogMaxLines — timestamped and in arrival order.

func webSiteContainerLogText(siteID string) []byte {
	var buf bytes.Buffer
	for _, row := range webSiteDockerLogs.Values(strings.ToLower(siteID)) {
		fmt.Fprintf(&buf, "%s %s\n", row["TimeGenerated"], row["Message"])
	}
	return buf.Bytes()
}

func registerWebContainerLogs(both func(string, string, http.HandlerFunc)) {
	// 204 when no output is retained.
	both("POST", "/containerlogs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		logs := webSiteContainerLogText(site.ID)
		if len(logs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(logs)
	})

	// One docker log file inside a ZIP, as App Service serves it.
	both("POST", "/containerlogs/zip/download", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		logs := webSiteContainerLogText(site.ID)
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
			AzureError(w, "InternalServerError", "Failed to assemble the container log archive: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})
}
