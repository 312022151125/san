package fs

import (
	"testing"
)

// TestComputeLineHash_Basic verifies the hash is always 3 lowercase letters.
func TestComputeLineHash_Basic(t *testing.T) {
	cases := []string{"hello world", "func foo() {", "    return nil", "", "x"}
	for _, line := range cases {
		h := ComputeLineHash(line)
		if len(h) != lineHashLen {
			t.Errorf("ComputeLineHash(%q) len=%d want %d", line, len(h), lineHashLen)
		}
		for _, c := range h {
			if c < 'a' || c > 'z' {
				t.Errorf("ComputeLineHash(%q) = %q contains non-lowercase-letter %q", line, h, c)
			}
		}
	}
}

// TestComputeLineHash_Deterministic verifies same input → same output.
func TestComputeLineHash_Deterministic(t *testing.T) {
	line := "func foo() {"
	a := ComputeLineHash(line)
	b := ComputeLineHash(line)
	if a != b {
		t.Errorf("non-deterministic: got %q and %q for same input", a, b)
	}
}

// TestComputeLineHash_WhitespaceInsensitive verifies all whitespace is stripped.
func TestComputeLineHash_WhitespaceInsensitive(t *testing.T) {
	cases := [][2]string{
		{"a b", "ab"},
		{"\tx\n", "x"},
		{"  func  foo  ", "funcfoo"},
		{"a\tb", "ab"},
	}
	for _, c := range cases {
		if ComputeLineHash(c[0]) != ComputeLineHash(c[1]) {
			t.Errorf("ComputeLineHash(%q) != ComputeLineHash(%q)", c[0], c[1])
		}
	}
}

// TestComputeLineHash_CRLF verifies trailing CR is stripped before hashing.
func TestComputeLineHash_CRLF(t *testing.T) {
	if ComputeLineHash("foo\r") != ComputeLineHash("foo") {
		t.Errorf("CRLF: trailing CR should not affect hash")
	}
}

// TestComputeLineHash_EmptyLine verifies empty line produces a valid hash.
func TestComputeLineHash_EmptyLine(t *testing.T) {
	h := ComputeLineHash("")
	if len(h) != lineHashLen {
		t.Errorf("empty line hash len=%d", len(h))
	}
}

// TestComputeLineHash_Unicode verifies Unicode content hashes deterministically.
func TestComputeLineHash_Unicode(t *testing.T) {
	a := ComputeLineHash("日本語テスト")
	b := ComputeLineHash("日本語テスト")
	if a != b {
		t.Errorf("unicode non-deterministic: %q %q", a, b)
	}
	if len(a) != lineHashLen {
		t.Errorf("unicode hash len=%d", len(a))
	}
}

// TestComputeFileHash_Deterministic verifies same input → same 4-hex-char tag.
func TestComputeFileHash_Deterministic(t *testing.T) {
	text := "one\ntwo\nthree\n"
	a := ComputeFileHash(text)
	b := ComputeFileHash(text)
	if a != b {
		t.Errorf("non-deterministic file hash: %q %q", a, b)
	}
	if len(a) != fileHashLen {
		t.Errorf("file hash len=%d want %d", len(a), fileHashLen)
	}
	for _, c := range a {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			t.Errorf("file hash %q has non-hex char %q", a, c)
		}
	}
}

// TestComputeFileHash_Sensitive verifies different content → different tag (with high probability).
func TestComputeFileHash_Sensitive(t *testing.T) {
	a := ComputeFileHash("one\ntwo\n")
	b := ComputeFileHash("one\ntwo\nthree\n")
	if a == b {
		t.Errorf("different content produced same hash %q", a)
	}
}

// TestComputeFileHash_CRLFNormalized verifies CRLF and LF produce the same tag.
func TestComputeFileHash_CRLFNormalized(t *testing.T) {
	lf := ComputeFileHash("line1\nline2\n")
	crlf := ComputeFileHash("line1\r\nline2\r\n")
	if lf != crlf {
		t.Errorf("CRLF/LF produced different tags: %q vs %q", crlf, lf)
	}
}

// TestComputeFileHash_TrailingWhitespaceNormalized verifies trailing spaces don't change the tag.
func TestComputeFileHash_TrailingWhitespaceNormalized(t *testing.T) {
	a := ComputeFileHash("line  \n")
	b := ComputeFileHash("line\n")
	if a != b {
		t.Errorf("trailing whitespace changed hash: %q vs %q", a, b)
	}
}

// TestFormatFileHeader verifies the @file path#TAG output format.
func TestFormatFileHeader(t *testing.T) {
	got := FormatFileHeader("foo/bar.go", "a1b2")
	want := "@file foo/bar.go#A1B2"
	if got != want {
		t.Errorf("FormatFileHeader = %q want %q", got, want)
	}
}

// TestFormatFileHeader_EmptyPath verifies empty path becomes ".".
func TestFormatFileHeader_EmptyPath(t *testing.T) {
	got := FormatFileHeader("", "ABCD")
	want := "@file .#ABCD"
	if got != want {
		t.Errorf("FormatFileHeader(%q) = %q want %q", "", got, want)
	}
}

// TestFormatHashline verifies the LINE#hash|content format.
func TestFormatHashline(t *testing.T) {
	h := ComputeLineHash("func foo() {")
	want := "10#" + h + "|func foo() {"
	got := FormatHashline(10, "func foo() {")
	if got != want {
		t.Errorf("FormatHashline = %q want %q", got, want)
	}
}

// TestParseHashRef_Valid verifies parsing of well-formed anchors.
func TestParseHashRef_Valid(t *testing.T) {
	cases := []struct {
		ref      string
		wantLine int
		wantHash string
	}{
		{"10#abc", 10, "abc"},
		{"1#xyz", 1, "xyz"},
		{"999#aaa", 999, "aaa"},
		{"  42#bcd  ", 42, "bcd"},
	}
	for _, c := range cases {
		line, hash, err := ParseHashRef(c.ref)
		if err != nil {
			t.Errorf("ParseHashRef(%q) err=%v", c.ref, err)
			continue
		}
		if line != c.wantLine || hash != c.wantHash {
			t.Errorf("ParseHashRef(%q) = (%d, %q) want (%d, %q)", c.ref, line, hash, c.wantLine, c.wantHash)
		}
	}
}

// TestParseHashRef_Invalid verifies malformed anchors return errors.
func TestParseHashRef_Invalid(t *testing.T) {
	bad := []string{"", "abc", "#abc", "0#abc", "-1#abc", "x#abc", "10#"}
	for _, ref := range bad {
		_, _, err := ParseHashRef(ref)
		if err == nil {
			t.Errorf("ParseHashRef(%q): expected error, got nil", ref)
		}
	}
}

// TestNormalizeFileTag verifies various input forms are canonicalized.
func TestNormalizeFileTag(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"A1B2", "A1B2"},
		{"a1b2", "A1B2"},
		{"  A1B2  ", "A1B2"},
		{"#A1B2", "A1B2"},
		{"@file src/app.go#A1B2", "A1B2"},
		{"@file src/app.go#a1b2", "A1B2"},
		{"", ""},
		{"#", ""},
		{"ABC", ""},     // 3 chars — too short
		{"A1B2C", ""},   // 5 chars — too long
		{"ZZZZ", ""},    // not hex
		{"A1G2", ""},    // G is not hex
	}
	for _, c := range cases {
		got := NormalizeFileTag(c.in)
		if got != c.want {
			t.Errorf("NormalizeFileTag(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeFileHashText verifies CRLF normalization and trailing space stripping.
func TestNormalizeFileHashText(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"line\r\n", "line\n"},
		{"line  \n", "line\n"},
		{"line\t\n", "line\n"},
		{"a\r\nb\r\n", "a\nb\n"},
		{"", ""},
	}
	for _, c := range cases {
		got := NormalizeFileHashText(c.in)
		if got != c.want {
			t.Errorf("NormalizeFileHashText(%q) = %q want %q", c.in, got, c.want)
		}
	}
}
