package repolylog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxSize = 1 << 20

type Log struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "archive.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, file: f}, nil
}
func (l *Log) Append(line string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	info, err := l.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 && info.Size()+int64(len(line)) > maxSize {
		if err := l.file.Close(); err != nil {
			return err
		}
		if err := os.Remove(l.path + ".2"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(l.path+".1", l.path+".2"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return err
		}
		l.file, err = os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
	}
	if _, err = l.file.WriteString(line); err != nil {
		return err
	}
	return l.file.Sync()
}
func (l *Log) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.file.Close() }
