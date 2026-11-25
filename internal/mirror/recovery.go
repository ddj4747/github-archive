package mirror

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// repairConfig replaces only a confirmed-unparseable config. Existing objects
// and refs are never changed. If their object format cannot be established
// safely, the repair fails closed and leaves the original file in place.
func repairConfig(ctx context.Context, mirrorPath, remote, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsAny(remote, "\r\n\x00") {
		return errors.New("invalid remote")
	}
	configPath := filepath.Join(mirrorPath, "config")
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("invalid config file")
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if token != "" && (bytes.Contains(original, []byte(token)) || bytes.Contains(original, []byte(base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))))) {
		return errors.New("config contains credentials and requires manual repair")
	}
	format, err := inferObjectFormat(mirrorPath)
	if err != nil {
		return err
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(remote)
	version, extension := "0", ""
	if format == 64 {
		version, extension = "1", "[extensions]\n\tobjectformat = sha256\n"
	}
	content := fmt.Sprintf("[core]\n\trepositoryformatversion = %s\n\tbare = true\n%s[remote \"origin\"]\n\turl = \"%s\"\n[gc]\n\tauto = 0\n\tautopacklimit = 0\n[fetch]\n\tprune = false\n", version, extension, quoted)

	backup, err := os.CreateTemp(mirrorPath, "config.corrupt-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err = writeAndClose(backup, original); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	// Once complete, keep the backup even if a subsequent validation fails.
	temp, err := os.CreateTemp(mirrorPath, ".config-repair-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = writeAndClose(temp, []byte(content)); err != nil {
		return err
	}
	if err = runGit(ctx, "", token, io.Discard, "config", "--file", name, "--list"); err != nil {
		return errors.New("replacement config failed validation")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	current, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(current, original) {
		return errors.New("config changed during repair")
	}
	return os.Rename(name, configPath)
}

func writeAndClose(f *os.File, data []byte) error {
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// inferObjectFormat examines refs without invoking Git against invalid config.
// A mix of hash lengths, malformed refs, or a mirror without refs is ambiguous.
func inferObjectFormat(mirrorPath string) (int, error) {
	length := 0
	consider := func(id string) error {
		if !objectID(id) {
			return errors.New("invalid ref object ID")
		}
		if length != 0 && length != len(id) {
			return errors.New("mixed object formats")
		}
		length = len(id)
		return nil
	}
	refs := filepath.Join(mirrorPath, "refs")
	err := filepath.WalkDir(refs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || strings.HasSuffix(entry.Name(), ".lock") {
			return errors.New("unsafe ref or active ref lock")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 256))
		if err != nil {
			return err
		}
		value := strings.TrimSpace(string(b))
		if strings.HasPrefix(value, "ref: ") {
			return nil
		}
		return consider(value)
	})
	if err != nil {
		return 0, err
	}
	f, err := os.Open(filepath.Join(mirrorPath, "packed-refs"))
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 4096), 8192)
		for s.Scan() {
			line := s.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "^") {
				if err := consider(line[1:]); err != nil {
					return 0, err
				}
				continue
			}
			id, _, ok := strings.Cut(line, " ")
			if !ok {
				return 0, errors.New("invalid packed ref")
			}
			if err := consider(id); err != nil {
				return 0, err
			}
		}
		if err := s.Err(); err != nil {
			return 0, err
		}
	}
	if length == 0 {
		return 0, errors.New("cannot determine object format without refs")
	}
	return length, nil
}
