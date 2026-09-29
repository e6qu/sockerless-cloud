package sim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// Payloads returns the payload store called name. A persistent server keeps it
// beside its database, so a restart serves the same bytes; otherwise it lives
// as long as the process, like the in-memory stores.
func (s *Server) Payloads(name string) (*blobstore.Payloads, error) {
	if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("payload store name %q is not a single path element", name)
	}
	var dir string
	if s.config.Persist {
		dir = filepath.Join(s.config.DataDir, "payloads", name)
	} else {
		temp, err := os.MkdirTemp("", "sim-payloads-"+name+"-")
		if err != nil {
			return nil, fmt.Errorf("payload store %s: %w", name, err)
		}
		dir = temp
	}
	return blobstore.OpenPayloads(dir)
}
