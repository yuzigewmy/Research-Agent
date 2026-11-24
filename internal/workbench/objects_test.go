package workbench

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestObjectReadsRequireCanonicalDigest(t *testing.T) {
	store := &Store{objectsDir: t.TempDir()}
	const text = "已保存的采样记录。"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if err := os.WriteFile(store.materialObject(digest), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if body, err := store.readObject(digest); err != nil || string(body) != text {
		t.Fatalf("canonical object read: %q, %v", body, err)
	}
	for _, bad := range []string{"../notes.txt", "notes.txt", digest[:63] + "G"} {
		if _, err := store.previewObject(bad, 1024); !errors.Is(err, errObjectDigest) {
			t.Errorf("invalid preview digest %q returned %v", bad, err)
		}
	}
}
