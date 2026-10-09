// Package filetail follows an open file by inode with bounded line storage.
// It drains an old inode before opening a replacement and detects ordinary
// truncation. Copytruncate concurrent with writes still has an inherent loss window.
package filetail

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

const MaxLine = 1 << 20

// Open rejects pipes/devices before opening so a configured non-file log cannot
// block a polling worker waiting for a pipe writer.
func Open(path string) (*os.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: log must be a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err = f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s: log must be a regular file", path)
	}
	return f, nil
}

type Reader struct {
	Path       string
	f          *os.File
	offset     int64
	partial    []byte
	skip       bool
	checkpoint []byte
	buf        [32 * 1024]byte
}

// New takes ownership of f. A nil file waits for creation, starting at zero.
func New(path string, f *os.File, offset int64, skipPartial bool) *Reader {
	r := &Reader{Path: path, f: f, offset: offset, skip: skipPartial}
	r.mark()
	return r
}

func (r *Reader) Close() error {
	if r.f != nil {
		err := r.f.Close()
		r.f = nil
		return err
	}
	return nil
}
func (r *Reader) Offset() int64 { return r.offset }

func (r *Reader) mark() {
	if r.f == nil || r.offset == 0 {
		r.checkpoint = nil
		return
	}
	n := min(r.offset, 64)
	r.checkpoint = make([]byte, n)
	if _, err := r.f.ReadAt(r.checkpoint, r.offset-n); err != nil {
		r.checkpoint = nil
	}
}

// Read consumes at most budget bytes and returns complete lines. more means
// unread content remains; callers can continue without sleeping, with cancellation.
func (r *Reader) Read(budget int) (lines []string, more bool, err error) {
	if r.f == nil {
		r.f, err = Open(r.Path)
		if err != nil {
			return nil, false, err
		}
	}
	fi, err := r.f.Stat()
	if err != nil {
		return nil, false, err
	}
	reset := fi.Size() < r.offset
	if !reset && len(r.checkpoint) > 0 {
		var check [64]byte
		n := len(r.checkpoint)
		_, e := r.f.ReadAt(check[:n], r.offset-int64(n))
		reset = e != nil || !bytes.Equal(check[:n], r.checkpoint)
	}
	if reset {
		r.offset = 0
		r.partial = nil
		r.skip = false
		r.checkpoint = nil
	}
	oldOffset := r.offset
	var errs []error
	for budget > 0 {
		if r.offset >= fi.Size() {
			current, e := os.Stat(r.Path)
			if e != nil {
				if !errors.Is(e, os.ErrNotExist) {
					errs = append(errs, e)
				}
				break
			} // retain/drain renamed inode while path is absent
			if os.SameFile(fi, current) {
				break
			}
			f, e := Open(r.Path)
			if e != nil {
				errs = append(errs, e)
				break
			}
			_ = r.f.Close()
			r.f = f
			r.offset = 0
			r.partial = nil
			r.skip = false
			r.checkpoint = nil
			fi, e = f.Stat()
			if e != nil {
				errs = append(errs, e)
				break
			}
		}
		n, e := r.f.ReadAt(r.buf[:min(budget, len(r.buf))], r.offset)
		if e != nil && e != io.EOF {
			errs = append(errs, e)
			break
		}
		if n == 0 {
			break
		}
		r.offset += int64(n)
		budget -= n
		for _, b := range r.buf[:n] {
			if b == '\n' {
				if !r.skip {
					lines = append(lines, string(bytes.TrimSuffix(r.partial, []byte{'\r'})))
				}
				r.partial = r.partial[:0]
				r.skip = false
			} else if !r.skip {
				if len(r.partial) >= MaxLine {
					r.partial = nil
					r.skip = true
					errs = append(errs, fmt.Errorf("%s: skipped line exceeding %d bytes", r.Path, MaxLine))
				} else {
					r.partial = append(r.partial, b)
				}
			}
		}
	}
	if r.offset != oldOffset {
		r.mark()
	}
	return lines, r.offset < fi.Size(), errors.Join(errs...)
}
