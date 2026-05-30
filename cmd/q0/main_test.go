package main

import (
	"strings"
	"testing"
)

func TestApplyEnvLines(t *testing.T) {
	out := map[string]string{
		"node_instance_profile_arn": "arn:aws:iam::111122223333:instance-profile/q0-node",
		"scripts_bucket":            "gauss-q0-scripts",
		"controller_private_ip":     "10.0.1.42",
		"efs_0_dns":                 "fs-abc.efs.us-east-1.amazonaws.com",
		"vpc_id":                    "vpc-123", // not mapped to an env — should be omitted
	}
	lines := applyEnvLines(out)
	joined := strings.Join(lines, "\n")

	wants := []string{
		"export Q0_INSTANCE_PROFILE_ARN=arn:aws:iam::111122223333:instance-profile/q0-node",
		"export Q0_SCRIPTS_BUCKET=gauss-q0-scripts",
		"export Q0_CONTROLLER_HOST=10.0.1.42",
		"# efs_0 = fs-abc.efs.us-east-1.amazonaws.com",
	}
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("missing line %q\n--- got ---\n%s", w, joined)
		}
	}
	// An unmapped output must not appear, and a manifest bucket we didn't set is omitted.
	if strings.Contains(joined, "vpc-123") {
		t.Error("unmapped output vpc_id should not be printed")
	}
	if strings.Contains(joined, "Q0_MANIFEST_BUCKET") {
		t.Error("absent manifest_bucket should be omitted")
	}
}

func TestApplyEnvLines_Empty(t *testing.T) {
	if lines := applyEnvLines(map[string]string{}); len(lines) != 0 {
		t.Errorf("empty outputs => no lines, got %v", lines)
	}
}

func TestApplyEnvLines_MultipleEFS(t *testing.T) {
	out := map[string]string{
		"efs_0_dns": "fs-0.efs.amazonaws.com",
		"efs_1_dns": "fs-1.efs.amazonaws.com",
	}
	lines := applyEnvLines(out)
	if len(lines) != 2 {
		t.Fatalf("want 2 efs comment lines, got %d: %v", len(lines), lines)
	}
}
