package buildnumber

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, raw string) *Format {
	t.Helper()
	f, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q) returned unexpected error: %v", raw, err)
	}
	return f
}

func TestRender_PlainCounter(t *testing.T) {
	f := mustParse(t, "1.4.%counter%")
	got := f.Render(87, 0, 0, time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	if got != "1.4.87" {
		t.Fatalf("got %q, want %q", got, "1.4.87")
	}
}

func TestRender_ZeroPaddedCounter(t *testing.T) {
	f := mustParse(t, "%counter:3%")
	got := f.Render(9, 0, 0, time.Now().UTC())
	if got != "009" {
		t.Fatalf("got %q, want %q", got, "009")
	}
}

func TestRender_CalendarFormat(t *testing.T) {
	f := mustParse(t, DefaultFormat) // %year%-%month%.%counter%
	got := f.Render(14, 0, 0, time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	if got != "2026-08.14" {
		t.Fatalf("got %q, want %q", got, "2026-08.14")
	}
}

func TestRender_SemVerFormat(t *testing.T) {
	f := mustParse(t, "%major%.%minor%.%counter%")
	got := f.Render(3, 2, 4, time.Now().UTC())
	if got != "2.4.3" {
		t.Fatalf("got %q, want %q", got, "2.4.3")
	}
}

func TestVersionKey_ResetsAcrossMonths(t *testing.T) {
	f := mustParse(t, DefaultFormat)
	july := f.VersionKey(0, 0, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	august := f.VersionKey(0, 0, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if july == august {
		t.Fatalf("expected different version keys across months, got %q for both", july)
	}
}

func TestVersionKey_StableWithinSameMonth(t *testing.T) {
	f := mustParse(t, DefaultFormat)
	a := f.VersionKey(0, 0, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	b := f.VersionKey(0, 0, time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC))
	if a != b {
		t.Fatalf("expected same version key within a month, got %q vs %q", a, b)
	}
}

func TestVersionKey_ConstantWithNoVolatileToken(t *testing.T) {
	f := mustParse(t, "1.4.%counter%")
	if f.HasVolatileToken() {
		t.Fatalf("format has no date/major/minor token, HasVolatileToken should be false")
	}
	a := f.VersionKey(0, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	b := f.VersionKey(0, 0, time.Date(2030, 12, 31, 0, 0, 0, 0, time.UTC))
	if a != b {
		t.Fatalf("expected a stable version key for a format with no volatile token, got %q vs %q", a, b)
	}
}

func TestParse_RejectsUnknownToken(t *testing.T) {
	if _, err := Parse("%bogus%.%counter%"); err == nil {
		t.Fatal("expected error for unknown token, got nil")
	}
}

func TestParse_RejectsMalformedWidth(t *testing.T) {
	if _, err := Parse("%counter:abc%"); err == nil {
		t.Fatal("expected error for non-numeric width, got nil")
	}
}

func TestParse_RejectsMissingCounter(t *testing.T) {
	if _, err := Parse("%year%-%month%"); err == nil {
		t.Fatal("expected error for a format with no counter token, got nil")
	}
}

func TestParse_RejectsDuplicateCounter(t *testing.T) {
	if _, err := Parse("%counter%-%counter%"); err == nil {
		t.Fatal("expected error for a format with two counter tokens, got nil")
	}
}

func TestParse_RejectsEmpty(t *testing.T) {
	if _, err := Parse(""); err == nil {
		t.Fatal("expected error for an empty format string, got nil")
	}
}

func TestParse_RejectsUnterminatedToken(t *testing.T) {
	if _, err := Parse("%counter%-v1.0%"); err == nil {
		t.Fatal("expected error for a stray trailing %, got nil")
	}
}
