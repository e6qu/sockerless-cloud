package main

import (
	"archive/zip"
	"bytes"
	"testing"
)

func webTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWebReadBackupArchiveKeepsOnlySiteContent(t *testing.T) {
	files, err := webReadBackupArchive(webTestZip(t, map[string]string{
		"site/wwwroot/index.html": "<p>",
		"/site/wwwroot/app/x.js":  "js",
		"database.sql":            "dump",
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = string(f.Data)
	}
	if len(got) != 2 || got["index.html"] != "<p>" || got["app/x.js"] != "js" {
		t.Fatalf("restored %v", got)
	}
}

func TestWebArchivesRefuseEntriesLeavingTheRoot(t *testing.T) {
	for _, name := range []string{"..", "../evil", "site/wwwroot/../../../evil"} {
		if _, err := webReadBackupArchive(webTestZip(t, map[string]string{name: "x"})); err == nil {
			t.Errorf("backup entry %q was accepted", name)
		}
	}
}
