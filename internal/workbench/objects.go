package workbench

import (
	"errors"
	"os"
)

// Publish a complete content-addressed object before committing its database record.
func (s *Store) publishObject(staged *os.File, digest string) error {
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	destination := s.materialObject(digest)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged.Name(), destination); err != nil {
		// On Windows, a concurrent upload can keep the identical destination open.
		if _, statErr := os.Stat(destination); statErr != nil {
			return err
		}
	}
	return nil
}
