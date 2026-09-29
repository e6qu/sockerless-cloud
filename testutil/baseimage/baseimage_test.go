package baseimage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pull that tests a simulator's own registry goes through
// registrytrust.PullFromRegistryUnderTest or its suite's docker wrapper.
func TestNoSuitePullsABaseImageItself(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, file := range []string{"baseimage.go", filepath.Join("..", "registrytrust", "registrytrust.go")} {
		path, err := filepath.Abs(file)
		if err != nil {
			t.Fatal(err)
		}
		allowed[path] = true
	}
	var offenders []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || allowed[path] {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(source), `"docker", `+`"pull"`) {
			relative, _ := filepath.Rel(root, path)
			offenders = append(offenders, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these files pull a base image without asking the host first; use baseimage.Ensure:\n%s", strings.Join(offenders, "\n"))
	}
}

func TestLocalRefSpellsTheDigestAsATag(t *testing.T) {
	cases := map[string]string{
		"public.ecr.aws/docker/library/alpine:3.22":           "public.ecr.aws/docker/library/alpine:3.22",
		"public.ecr.aws/aws-dynamodb-local/x@sha256:ff89bd48": "public.ecr.aws/aws-dynamodb-local/x:sha256-ff89bd48",
	}
	for image, want := range cases {
		if got := LocalRef(image); got != want {
			t.Errorf("LocalRef(%q) = %q, want %q", image, got, want)
		}
	}
}
