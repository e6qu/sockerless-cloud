package doctest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRefusesAnUnannotatedShellBlock(t *testing.T) {
	_, err := Parse(writeDoc(t, "Intro.\n\n```sh\necho hi\n```\n"))
	if err == nil || !strings.Contains(err.Error(), "no doctest annotation") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRefusesAnAnnotationWithoutABlock(t *testing.T) {
	_, err := Parse(writeDoc(t, "<!-- doctest: run -->\nSome prose.\n"))
	if err == nil || !strings.Contains(err.Error(), "names no block") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRefusesASkipWithoutAReason(t *testing.T) {
	_, err := Parse(writeDoc(t, "<!-- doctest: skip -->\n```sh\nrm -rf /\n```\n"))
	if err == nil || !strings.Contains(err.Error(), "needs reason") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLeavesOtherLanguagesAlone(t *testing.T) {
	doc, err := Parse(writeDoc(t, "```go\nfmt.Println()\n```\n\n```text\n127.0.0.1 x\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Shared) != 0 {
		t.Fatalf("steps = %+v", doc.Shared)
	}
}

func TestParseSplitsParts(t *testing.T) {
	doc, err := Parse(writeDoc(t, strings.Join([]string{
		"<!-- doctest: run -->", "```sh", "echo setup", "```",
		"<!-- doctest: part name=one -->",
		"<!-- doctest: run expect=\"a b\" -->", "```bash", "echo a b", "```",
		"<!-- doctest: stop id=x -->",
		"<!-- doctest: part name=two -->",
		"<!-- doctest: skip reason=\"needs a fork\" -->", "```shell", "git push", "```",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Shared) != 1 || len(doc.Parts) != 2 || len(doc.Parts[0].Steps) != 2 || doc.Parts[1].Steps[0].Kind != Skip {
		t.Fatalf("doc = %+v", doc)
	}
	if got := doc.Parts[0].Steps[0].Expect; len(got) != 1 || got[0] != "a b" {
		t.Fatalf("expect = %q", got)
	}
}

// TestSessionCarriesStateAndRunsBackgroundBlocks drives a document through a
// session: a variable and a directory carry between blocks, and a background
// block keeps serving until the document stops it.
func TestSessionCarriesStateAndRunsBackgroundBlocks(t *testing.T) {
	doc, err := Parse(writeDoc(t, strings.Join([]string{
		"<!-- doctest: run -->", "```sh", "mkdir work && cd work", "export GREETING=hello", "```",
		"<!-- doctest: background id=srv ready=\"^up$\" -->", "```sh",
		"mkfifo pipe; echo up; read -r line < pipe; echo \"got $line\" > got", "```",
		"<!-- doctest: run expect=\"^hello from .*/work\\n$\" -->", "```sh", "echo \"$GREETING from $PWD\"; echo ping > pipe", "```",
		"<!-- doctest: stop id=srv -->",
		"<!-- doctest: run expect=\"got\" expect=\"ping\" -->", "```sh", "cat got", "```",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	RunDoc(t, doc, os.Environ())
}
