package repolylog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotationAndAppend(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", maxSize/2)
	for i := 0; i < 8; i++ {
		if err = l.Append(line); err != nil {
			t.Fatal(err)
		}
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d files", len(entries))
	}
	for _, name := range []string{"archive.log", "archive.log.1", "archive.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
