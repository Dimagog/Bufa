package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{" y \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false}, // EOF: non-interactive stdin must not nuke
		{"yeah\n", false},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		if got := confirm(strings.NewReader(tt.input), &out, "Nuke it?"); got != tt.want {
			t.Errorf("confirm(%q) = %v, want %v", tt.input, got, tt.want)
		}
		if !strings.Contains(out.String(), "Nuke it? [y/N] ") {
			t.Errorf("prompt not printed, got %q", out.String())
		}
	}
}
