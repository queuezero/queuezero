package bootstrap

import (
	"reflect"
	"testing"
)

func TestMountSpec_RoundTrip(t *testing.T) {
	in := []Mount{
		{DNS: "fs-0.efs.us-east-1.amazonaws.com", Path: "/shared"},
		{DNS: "fs-1.efs.us-east-1.amazonaws.com", Path: "/scratch"},
	}
	spec := FormatMountSpec(in)
	want := "fs-0.efs.us-east-1.amazonaws.com:/shared,fs-1.efs.us-east-1.amazonaws.com:/scratch"
	if spec != want {
		t.Errorf("FormatMountSpec = %q, want %q", spec, want)
	}
	got := ParseMountSpec(spec)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, in)
	}
}

func TestMountSpec_Empty(t *testing.T) {
	if s := FormatMountSpec(nil); s != "" {
		t.Errorf("empty format = %q, want empty", s)
	}
	if m := ParseMountSpec(""); m != nil {
		t.Errorf("empty parse = %+v, want nil", m)
	}
	if m := ParseMountSpec("   "); m != nil {
		t.Errorf("whitespace parse = %+v, want nil", m)
	}
}

func TestFormatMountSpec_SkipsIncomplete(t *testing.T) {
	in := []Mount{{DNS: "a", Path: "/x"}, {DNS: "", Path: "/y"}, {DNS: "b", Path: ""}}
	if s := FormatMountSpec(in); s != "a:/x" {
		t.Errorf("FormatMountSpec skipping incomplete = %q, want a:/x", s)
	}
}

func TestParseMountSpec_SkipsMalformed(t *testing.T) {
	got := ParseMountSpec("a:/x, nocolon , :/onlypath, dns:, b:/y")
	want := []Mount{{DNS: "a", Path: "/x"}, {DNS: "b", Path: "/y"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMountSpec malformed = %+v, want %+v", got, want)
	}
}

func TestMountPaths(t *testing.T) {
	in := []Mount{{DNS: "a", Path: "/shared"}, {DNS: "b", Path: "/scratch"}}
	if p := MountPaths(in); p != "/shared,/scratch" {
		t.Errorf("MountPaths = %q, want /shared,/scratch", p)
	}
}
