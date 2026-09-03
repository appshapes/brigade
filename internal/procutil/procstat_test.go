package procutil

import (
	"strings"
	"testing"
)

// statLine builds a /proc/<pid>/stat line with the given pid, comm, state
// and start time (field 22), padding the other fields with distinct
// numbers so an off-by-one in the field index reads a wrong value rather
// than a coincidentally right one.
func statLine(pid, comm, state, start string) string {
	fields := []string{pid, "(" + comm + ")", state}
	// fields 4..21
	for i := 4; i <= 21; i++ {
		fields = append(fields, "1"+strings.Repeat("0", i-3))
	}
	fields = append(fields, start) // 22
	// a realistic tail: fields 23..52
	for i := 23; i <= 52; i++ {
		fields = append(fields, "7")
	}
	return strings.Join(fields, " ") + "\n"
}

func TestParseProcStat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		line  string
		want  procStat
		valid bool
	}{
		{"running sleep", statLine("4242", "sleep", "S", "123456789"), procStat{4242, 'S', "123456789"}, true},
		{"zombie", statLine("4242", "sleep", "Z", "5"), procStat{4242, 'Z', "5"}, true},
		{"comm with spaces and parens", statLine("7", "sleep 1) (x", "R", "99"), procStat{7, 'R', "99"}, true},
		{"comm that is only a paren", statLine("7", ")", "R", "99"), procStat{7, 'R', "99"}, true},
		{"comm with a fake state after a paren", statLine("7", "Z) Z 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19", "S", "31"), procStat{7, 'S', "31"}, true},
		{"kernel-thread comm", statLine("2", "kthreadd", "S", "3"), procStat{2, 'S', "3"}, true},
		{"no trailing newline", strings.TrimSuffix(statLine("1", "init", "S", "8"), "\n"), procStat{1, 'S', "8"}, true},
		{"empty", "", procStat{}, false},
		{"no parens", "1 init S 1 2 3", procStat{}, false},
		{"close before open", "1 ) init ( S", procStat{}, false},
		{"pid not a number", statLine("x", "init", "S", "8"), procStat{}, false},
		{"pid zero", statLine("0", "init", "S", "8"), procStat{}, false},
		{"too few fields", "1 (init) S 1 2 3 4 5\n", procStat{}, false},
		{"state longer than one byte", statLine("1", "init", "SS", "8"), procStat{}, false},
		{"start time not numeric", statLine("1", "init", "S", "8x"), procStat{}, false},
		{"start time negative", statLine("1", "init", "S", "-8"), procStat{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseProcStat([]byte(tc.line))
			if tc.valid && err != nil {
				t.Fatalf("parseProcStat(%q) = %v, want success", tc.line, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("parseProcStat(%q) = %+v, want an error", tc.line, got)
			}
			if got != tc.want {
				t.Fatalf("parseProcStat(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

// TestParseProcStatFieldIndexIsTwentyTwo pins the index arithmetic: field
// 22 counted from the pid is index 19 of the list after the last `)`.
// A parser that took index 18 or 20 would read the padding, which is
// deliberately distinct for every position.
func TestParseProcStatFieldIndexIsTwentyTwo(t *testing.T) {
	t.Parallel()
	line := statLine("1", "init", "S", "424242")
	got, err := parseProcStat([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if got.startTicks != "424242" {
		t.Fatalf("startTicks = %q, want the value placed in field 22", got.startTicks)
	}
	all := strings.Fields(line)
	if all[21] != "424242" {
		t.Fatalf("the fixture put the value at field %d, not 22", 1+indexOf(all, "424242"))
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
