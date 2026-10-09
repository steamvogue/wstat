package filetail

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBudgetPartialRotationAndTruncate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "access")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(p, "a\nb\npart")
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	r := New(p, f, 0, false)
	defer func() { _ = r.Close() }()
	lines, more, err := r.Read(2)
	if err != nil || !more || !reflect.DeepEqual(lines, []string{"a"}) || r.Offset() != 2 {
		t.Fatal(lines, more, err)
	}
	lines, more, err = r.Read(100)
	if err != nil || more || !reflect.DeepEqual(lines, []string{"b"}) {
		t.Fatal(lines, more, err)
	}
	if err = os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	old, err := os.OpenFile(p+".1", os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.WriteString("ial\n")
	_ = old.Close()
	if err != nil {
		t.Fatal(err)
	}
	write(p, "replacement\n")
	lines, _, err = r.Read(100)
	if err != nil || !reflect.DeepEqual(lines, []string{"partial", "replacement"}) {
		t.Fatal(lines, err)
	}
	write(p, "same-length\n") // same-size copytruncate/regrowth, different checkpoint
	lines, _, err = r.Read(100)
	if err != nil || !reflect.DeepEqual(lines, []string{"same-length"}) {
		t.Fatal(lines, err)
	}
}

func TestOversizedLineRecoversWithinBudget(t *testing.T) {
	p := filepath.Join(t.TempDir(), "access")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", MaxLine+100)+"\ngood\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(p, nil, 0, false)
	defer func() { _ = r.Close() }()
	var all []string
	warned := false
	for {
		before := r.Offset()
		lines, more, err := r.Read(32768)
		all = append(all, lines...)
		warned = warned || err != nil
		if r.Offset()-before > 32768 {
			t.Fatal("byte budget exceeded")
		}
		if !more {
			break
		}
	}
	if !warned || !reflect.DeepEqual(all, []string{"good"}) {
		t.Fatal(all, warned)
	}
}

func TestWaitForCreationAndNonRegular(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing")
	r := New(p, nil, 0, false)
	defer func() { _ = r.Close() }()
	if _, _, err := r.Read(100); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lines, _, err := r.Read(100)
	if err != nil || !reflect.DeepEqual(lines, []string{"new"}) {
		t.Fatal(lines, err)
	}
	if _, err = Open(t.TempDir()); err == nil {
		t.Fatal("directory accepted as a log")
	}
}
