package workbench

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
)

var errObjectDigest = errors.New("资料对象摘要无效。")

func validObjectDigest(digest string) bool {
	return len(digest) == 2*sha256.Size && !strings.ContainsFunc(digest, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	})
}

func (s *Store) openObject(digest string) (*os.File, error) {
	if !validObjectDigest(digest) {
		return nil, errObjectDigest
	}
	return os.OpenInRoot(s.objectsDir, digest)
}

func (s *Store) readObject(digest string) ([]byte, error) {
	file, err := s.openObject(digest)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

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
