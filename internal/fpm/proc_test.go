package fpm

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestProcStatUnitsAndMalformedFields(t *testing.T) {
	f := make([]string, 22)
	for i := range f {
		f[i] = "0"
	}
	f[0] = "S"
	f[11] = "13"
	f[12] = "7"
	f[21] = "3"
	s := "123 (php-fpm: pool with spaces) " + strings.Join(f, " ")
	rss, cpu, ok := parseProcStat(s)
	if !ok || rss/1024 != int64(3*os.Getpagesize()/1024) || cpu != 20 {
		t.Fatal(rss, cpu, ok)
	}
	for _, s := range []string{"", "123 (short) " + strings.Join(f[:21], " "), fmt.Sprintf("123 (invalid) %s", strings.Join(append(f[:21:21], "bad"), " "))} {
		if _, _, ok := parseProcStat(s); ok {
			t.Fatal("malformed stat accepted", s)
		}
	}
}
