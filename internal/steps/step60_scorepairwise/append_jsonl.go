package step60_scorepairwise

import (
	"fmt"
	"os"
	"path/filepath"

	internalio "github.com/nishimoto265/harnest/internal/io"
)

func appendJSONLWithParentDirSync(path string, record any) error {
	if err := internalio.AppendJSONL(path, record); err != nil {
		return fmt.Errorf("append jsonl %s: %w", path, err)
	}

	parentDir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent dir %s: %w", filepath.Dir(path), err)
	}
	defer parentDir.Close()

	if err := parentDir.Sync(); err != nil {
		return fmt.Errorf("sync parent dir %s: %w", filepath.Dir(path), err)
	}
	return nil
}
