package admin

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const tmpInfix = ".tmp"

// prepareDir creates dir as root 0700 if needed and refuses anything that is
// not a real directory (a symlink would redirect the hash and the sessions).
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs x
			return err
		}
	}
	// A crash between writing and publishing leaves temp files behind.
	stale, _ := filepath.Glob(filepath.Join(dir, ".*"+tmpInfix+"*"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return nil
}

// writeTemp writes data to a new 0600 file next to final, synced, and
// returns its path. The caller publishes it with link(2) or rename(2), so a
// reader never sees a partial file.
func writeTemp(final string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(final), "."+filepath.Base(final)+tmpInfix)
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(name)
		return "", werr
	}
	return name, nil
}

// writeFileAtomic replaces path with data.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// publishExclusive gives tmp the name final only if final does not exist:
// link(2) has the semantics of O_CREAT|O_EXCL on final, without ever exposing
// a half-written file.
func publishExclusive(tmp, final string) error {
	err := os.Link(tmp, final)
	_ = os.Remove(tmp)
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(final))
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // daemon-owned directory
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

// readSmallFile reads a daemon-owned file without following a symlink.
// A missing file returns (nil, false, nil).
func readSmallFile(path string, limit int64) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // daemon-owned path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(b)) > limit {
		return nil, true, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, true, nil
}
