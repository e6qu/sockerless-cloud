package sim

import (
	"fmt"
	"io/fs"
	"os"
)

// ConsoleBundle opens the built console bundle in dir, whose index.html sits
// at its root.
func ConsoleBundle(dir string) (fs.FS, error) {
	bundle := os.DirFS(dir)
	if _, err := fs.Stat(bundle, "index.html"); err != nil {
		return nil, fmt.Errorf("console directory %s holds no console bundle: %w", dir, err)
	}
	return bundle, nil
}
