package passwords

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	got := Parse([]byte("one\r\n\n  two  \none\n#three\n\nfour"))
	want := []string{"one", "two", "#three", "four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFileRejectsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	write(t, p, "\n \n")
	if _, err := OpenFile(p, nil); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, err := OpenFile(p+".missing", nil); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	write(t, p, "a\nb\n")
	f, err := OpenFile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.List(), []string{"a", "b"}) {
		t.Fatalf("initial %q", f.List())
	}

	// unchanged content: no wake-up
	ch := f.Changed()
	if changed, err := f.Reload(); changed || err != nil {
		t.Fatalf("noop reload changed=%v err=%v", changed, err)
	}
	select {
	case <-ch:
		t.Fatal("Changed fired without a change")
	default:
	}

	write(t, p, "b\nc\n")
	if changed, err := f.Reload(); !changed || err != nil {
		t.Fatalf("reload changed=%v err=%v", changed, err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("Changed not closed after a change")
	}
	if !reflect.DeepEqual(f.List(), []string{"b", "c"}) {
		t.Fatalf("after reload %q", f.List())
	}

	// a truncated/empty file keeps the old list
	write(t, p, "")
	if _, err := f.Reload(); err == nil {
		t.Fatal("empty reload not reported")
	}
	if !reflect.DeepEqual(f.List(), []string{"b", "c"}) {
		t.Fatalf("list lost on empty file: %q", f.List())
	}
}

func TestRunPicksUpChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	write(t, p, "a\n")
	f, err := OpenFile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithCancel()
	defer cancel()
	go f.Run(ctx, 10*time.Millisecond)

	ch := f.Changed()
	write(t, p, "a\nz\n")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not reload the file")
	}
	if !reflect.DeepEqual(f.List(), []string{"a", "z"}) {
		t.Fatalf("got %q", f.List())
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
