package blobstore

import (
	"errors"
	"fmt"
)

// OpenCurrent opens the contents row references, so a ranged read touches only
// its range. An overwrite between reading the row and opening its file
// removes the file; reload reads the row again then, and the row it returns is
// the one whose contents are opened, so a caller describes the contents it
// serves. name identifies the row in errors.
func OpenCurrent[T any](p *Payloads, row T, ref func(T) string, reload func(T) (T, bool), name string) (T, Reader, error) {
	for attempt := 0; attempt < 2; attempt++ {
		file, err := p.Open(ref(row))
		if err == nil {
			return row, file, nil
		}
		if !errors.Is(err, ErrPayloadGone) {
			return row, nil, err
		}
		current, ok := reload(row)
		if !ok || ref(current) == ref(row) {
			return row, nil, fmt.Errorf("the contents of %s: %w", name, err)
		}
		row = current
	}
	return row, nil, fmt.Errorf("the contents of %s changed twice while being read", name)
}
