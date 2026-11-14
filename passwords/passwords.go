// Package passwords holds the set of pre-shared passwords an exit server
// accepts, and can keep that set in sync with a file on disk.
//
// A Source is a snapshot provider: List returns the current passwords and
// Changed returns a channel that is closed the next time the set changes, so a
// consumer can react (for example by dropping sessions whose password was
// removed) without polling.
package passwords

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Source provides the current set of accepted passwords.
type Source interface {
	// List returns the current passwords. The slice must not be modified.
	List() []string
	// Changed returns a channel that is closed the next time the set
	// changes. Take it BEFORE calling List so no update is missed:
	//
	//	for { ch := s.Changed(); use(s.List()); <-ch }
	Changed() <-chan struct{}
}

// Static is a fixed, never-changing Source.
type Static []string

func (s Static) List() []string { return s }

// Changed returns a channel that is never closed.
func (s Static) Changed() <-chan struct{} { return nil }

// Parse reads one password per line. Surrounding whitespace (including a
// trailing \r from CRLF files) is trimmed, empty lines are skipped and
// duplicates are dropped; the first occurrence keeps its position. There is
// no comment syntax, so any non-empty line is a valid password.
func Parse(data []byte) []string {
	var out []string
	seen := map[string]struct{}{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// File is a Source backed by a file with one password per line. Call Run to
// keep it up to date, or Reload to refresh it on demand (e.g. on SIGHUP).
type File struct {
	path string
	logf func(string, ...any)

	mu      sync.RWMutex
	list    []string
	changed chan struct{}
}

// OpenFile loads path once and fails if it cannot be read or contains no
// passwords: starting a server that nobody can authenticate to is a mistake
// worth reporting at once. logf may be nil.
func OpenFile(path string, logf func(string, ...any)) (*File, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	f := &File{path: path, logf: logf, changed: make(chan struct{})}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("passwords: %w", err)
	}
	f.list = Parse(data)
	if len(f.list) == 0 {
		return nil, fmt.Errorf("passwords: %s contains no passwords", path)
	}
	return f, nil
}

func (f *File) List() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.list
}

func (f *File) Changed() <-chan struct{} {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.changed
}

// Reload re-reads the file and, if the set of passwords differs, swaps it in
// and wakes everyone waiting on Changed. A read error or an empty result keeps
// the previous set: an editor that truncates the file before writing it, or a
// typo, must not lock every client out. It reports whether the set changed.
func (f *File) Reload() (bool, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return false, fmt.Errorf("passwords: %w (keeping previous list)", err)
	}
	next := Parse(data)
	if len(next) == 0 {
		return false, errors.New("passwords: file has no passwords (keeping previous list)")
	}
	f.mu.Lock()
	if equal(f.list, next) {
		f.mu.Unlock()
		return false, nil
	}
	prev := f.list
	old := f.changed
	f.list = next
	f.changed = make(chan struct{})
	f.mu.Unlock()
	close(old)
	f.logf("passwords: reloaded %s: %d password(s), was %d", f.path, len(next), len(prev))
	return true, nil
}

// Run polls the file every interval (default 2s) until ctx ends. Polling the
// content, rather than watching inode events, also works through editors that
// replace the file, bind-mounted Docker files and network filesystems. Reload
// errors are logged, once per distinct error, until the file is good again.
func (f *File) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := f.Reload(); err != nil {
				if msg := err.Error(); msg != lastErr {
					f.logf("%s", msg)
					lastErr = msg
				}
			} else {
				lastErr = ""
			}
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
