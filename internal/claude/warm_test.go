package claude

import "testing"

// RealWarmer spawns a process and is exercised only end to end, but firstLine
// shapes what lands in the per-account report, so it is unit-tested here.
func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"blank lines only", "\n  \n\t\n", ""},
		{"single line", "boom", "boom"},
		{"trims surrounding space", "  boom  ", "boom"},
		{"skips leading blanks", "\n\n  first real line\nsecond", "first real line"},
		{"stops at first line", "one\ntwo\nthree", "one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in); got != tc.want {
				t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
