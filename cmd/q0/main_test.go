package main

import (
	"strings"
	"testing"

	"github.com/queuezero/queuezero/internal/spec"
)

func TestApplyEnvLines(t *testing.T) {
	cl := &spec.Cluster{
		Name: "gauss",
		Storage: []spec.StorageSpec{
			{Kind: "efs", MountPath: "/shared"},
		},
	}
	out := map[string]string{
		"node_instance_profile_arn": "arn:aws:iam::111122223333:instance-profile/q0-node",
		"scripts_bucket":            "gauss-q0-scripts",
		"controller_private_ip":     "10.0.1.42",
		"efs_0_dns":                 "fs-abc.efs.us-east-1.amazonaws.com",
		"vpc_id":                    "vpc-123", // not mapped to an env — should be omitted
	}
	joined := strings.Join(applyEnvLines(cl, out), "\n")

	wants := []string{
		"export Q0_INSTANCE_PROFILE_ARN=arn:aws:iam::111122223333:instance-profile/q0-node",
		"export Q0_SCRIPTS_BUCKET=gauss-q0-scripts",
		"export Q0_CONTROLLER_HOST=10.0.1.42",
		"export Q0_MOUNT_SPEC=fs-abc.efs.us-east-1.amazonaws.com:/shared",
	}
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("missing line %q\n--- got ---\n%s", w, joined)
		}
	}
	if strings.Contains(joined, "vpc-123") {
		t.Error("unmapped output vpc_id should not be printed")
	}
	if strings.Contains(joined, "Q0_MANIFEST_BUCKET") {
		t.Error("absent manifest_bucket should be omitted")
	}
}

func TestApplyEnvLines_Empty(t *testing.T) {
	if lines := applyEnvLines(&spec.Cluster{}, map[string]string{}); len(lines) != 0 {
		t.Errorf("empty outputs => no lines, got %v", lines)
	}
}

func TestApplyEnvLines_MultipleEFS(t *testing.T) {
	cl := &spec.Cluster{Storage: []spec.StorageSpec{
		{Kind: "efs", MountPath: "/shared"},
		{Kind: "efs", MountPath: "/scratch"},
	}}
	out := map[string]string{
		"efs_0_dns": "fs-0.efs.amazonaws.com",
		"efs_1_dns": "fs-1.efs.amazonaws.com",
	}
	joined := strings.Join(applyEnvLines(cl, out), "\n")
	want := "export Q0_MOUNT_SPEC=fs-0.efs.amazonaws.com:/shared,fs-1.efs.amazonaws.com:/scratch"
	if !strings.Contains(joined, want) {
		t.Errorf("missing %q\n--- got ---\n%s", want, joined)
	}
}

// A storage entry whose efs DNS output is missing is skipped (not half-emitted).
func TestApplyEnvLines_MissingDNSSkipped(t *testing.T) {
	cl := &spec.Cluster{Storage: []spec.StorageSpec{{Kind: "efs", MountPath: "/shared"}}}
	joined := strings.Join(applyEnvLines(cl, map[string]string{}), "\n")
	if strings.Contains(joined, "Q0_MOUNT_SPEC") {
		t.Error("no efs DNS output => no Q0_MOUNT_SPEC")
	}
}
