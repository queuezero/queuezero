package slurm

import (
	"context"
	"reflect"
	"testing"
)

func TestExpandHostlist(t *testing.T) {
	cases := map[string][]string{
		"gpu-[001-004]": {"gpu-001", "gpu-002", "gpu-003", "gpu-004"},
		"gpu-042":       {"gpu-042"},
		"n-[7-9]":       {"n-7", "n-8", "n-9"},
		"a,b,c":         {"a", "b", "c"},
		"cpu-[01-03]":   {"cpu-01", "cpu-02", "cpu-03"},
	}
	for in, want := range cases {
		got, err := expandHostlist(in)
		if err != nil {
			t.Errorf("expandHostlist(%q): %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("expandHostlist(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExpandHostlist_Malformed(t *testing.T) {
	if _, err := expandHostlist("gpu-[abc]"); err == nil {
		t.Error("expected error for non-numeric range")
	}
	if _, err := expandHostlist("gpu-[001"); err == nil {
		t.Error("expected error for unclosed bracket")
	}
}

// fallbackScontrol exercises the in-process path (no real scontrol binary).
func TestExecScontrol_FallbackExpands(t *testing.T) {
	s := &execScontrol{available: false}
	got, err := s.ShowHostnames(context.Background(), "gpu-[001-002]")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"gpu-001", "gpu-002"}) {
		t.Errorf("fallback expansion wrong: %v", got)
	}
	// UpdateNode is a no-op when unavailable.
	if err := s.UpdateNode(context.Background(), "gpu-001", "down", "x"); err != nil {
		t.Errorf("UpdateNode no-op should not error: %v", err)
	}
}
