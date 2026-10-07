package agent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Upload keeps a file sent from a device under this agent's state directory,
// one directory per session, and returns its absolute path.
//
// The name is the device's, reduced to safe characters and prefixed with the
// time: it says what the file was without letting a name like "../../.bashrc"
// decide where it lands. Not in the session's own directory, which is
// somebody's working tree.
func (s *Session) Upload(name string, body io.Reader) (string, error) {
	base := unsafeName.ReplaceAllString(filepath.Base(strings.TrimSpace(name)), "_")
	base = strings.Trim(base, "._")
	if base == "" {
		base = "file"
	}
	dir := filepath.Join(s.m.dir, "uploads", s.Name())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Stamped to the second, and numbered when two arrive in the same one.
	stamp := time.Now().Format("20060102-150405")
	var path string
	var f *os.File
	for i := 0; ; i++ {
		path = filepath.Join(dir, stamp+"-"+base)
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d-%s", stamp, i, base))
		}
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return "", err
		}
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("an empty file")
	}
	if err != nil {
		os.Remove(path)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return "", fmt.Errorf("the file is larger than %d MB", MaxUpload>>20)
		}
		return "", err
	}
	return path, nil
}
