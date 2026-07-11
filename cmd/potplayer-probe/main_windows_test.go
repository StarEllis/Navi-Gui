//go:build windows

package main

import (
	"math"
	"testing"
)

func TestIsPotPlayerClass(t *testing.T) {
	tests := []struct {
		className string
		want      bool
	}{
		{className: "PotPlayer64", want: true},
		{className: "PotPlayer", want: true},
		{className: "potplayer64", want: false},
		{className: "Afx:00007FFF", want: false},
	}

	for _, test := range tests {
		if got := isPotPlayerClass(test.className); got != test.want {
			t.Errorf("isPotPlayerClass(%q) = %v, want %v", test.className, got, test.want)
		}
	}
}

func TestParseSelection(t *testing.T) {
	selection, err := parseSelection(" 2 ")
	if err != nil || selection != 2 {
		t.Fatalf("parseSelection returned (%d, %v), want (2, nil)", selection, err)
	}

	for _, input := range []string{"", "0", "-1", "not-a-number"} {
		if _, err := parseSelection(input); err == nil {
			t.Errorf("parseSelection(%q) succeeded unexpectedly", input)
		}
	}
}

func TestSignedStatus(t *testing.T) {
	if got := signedStatus(^uintptr(0)); got != -1 {
		t.Fatalf("signedStatus(all bits set) = %d, want -1", got)
	}
	if got := signedStatus(2); got != 2 {
		t.Fatalf("signedStatus(2) = %d, want 2", got)
	}
}

func TestSeekTarget(t *testing.T) {
	tests := []struct {
		name    string
		current uint64
		total   uint64
		want    uint64
		ok      bool
	}{
		{name: "unknown total", current: 2_000, total: 0, want: 12_000, ok: true},
		{name: "within total", current: 2_000, total: 20_000, want: 12_000, ok: true},
		{name: "cap at total", current: 15_000, total: 20_000, want: 20_000, ok: true},
		{name: "at total", current: 20_000, total: 20_000, want: 0, ok: false},
		{name: "overflow", current: math.MaxUint64 - 9_999, total: 0, want: 0, ok: false},
	}

	for _, test := range tests {
		got, ok := seekTarget(test.current, test.total)
		if got != test.want || ok != test.ok {
			t.Errorf("%s: seekTarget(%d, %d) = (%d, %v), want (%d, %v)", test.name, test.current, test.total, got, ok, test.want, test.ok)
		}
	}
}
