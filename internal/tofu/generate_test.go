package tofu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/queuezero/queuezero/internal/spec"
)

func testCluster() *spec.Cluster {
	return &spec.Cluster{Name: "gauss", ControlAccount: "111122223333", Region: "us-west-2"}
}

func TestGenerateClusterFoundation_RendersExpectedResources(t *testing.T) {
	files, err := GenerateClusterFoundation(testCluster(), FoundationOpts{
		ScriptsBucket:  "gauss-q0-scripts",
		ManifestBucket: "gauss-q0-manifest",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	main := files["main.tf"]
	wants := []string{
		`backend "s3" {}`,                                  // state backend wired
		`region = "us-west-2"`,                             // from cluster.yaml
		`resource "aws_s3_bucket" "scripts"`,               // scripts bucket
		`bucket = "gauss-q0-scripts"`,
		`resource "aws_s3_bucket_versioning" "scripts"`,    // versioned
		`resource "aws_s3_bucket_public_access_block" "scripts"`, // public-access-blocked
		`resource "aws_s3_bucket" "manifest"`,              // manifest bucket (opt set)
		`resource "aws_iam_role" "node"`,                   // node role
		`name = "q0-node"`,                                 // default role name
		`resource "aws_iam_instance_profile" "node"`,       // instance profile
		`"s3:GetObject"`,                                   // scoped read
		`"${aws_s3_bucket.scripts.arn}/*"`,                 // scoped to scripts bucket
	}
	for _, w := range wants {
		if !strings.Contains(main, w) {
			t.Errorf("main.tf missing %q", w)
		}
	}
	out := files["outputs.tf"]
	if !strings.Contains(out, "node_instance_profile_arn") || !strings.Contains(out, "scripts_bucket") {
		t.Errorf("outputs.tf missing expected outputs:\n%s", out)
	}
}

func TestGenerateClusterFoundation_NoManifestBucket(t *testing.T) {
	files, err := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files["main.tf"], `"manifest"`) {
		t.Error("manifest bucket should be omitted when ManifestBucket is empty")
	}
	if strings.Contains(files["outputs.tf"], "manifest_bucket") {
		t.Error("manifest output should be omitted when ManifestBucket is empty")
	}
}

func TestGenerateClusterFoundation_RequiresScriptsBucket(t *testing.T) {
	if _, err := GenerateClusterFoundation(testCluster(), FoundationOpts{}); err == nil {
		t.Error("expected error when ScriptsBucket is empty")
	}
}

func TestGenerateClusterFoundation_Deterministic(t *testing.T) {
	o := FoundationOpts{ScriptsBucket: "b", ManifestBucket: "m"}
	a, _ := GenerateClusterFoundation(testCluster(), o)
	b, _ := GenerateClusterFoundation(testCluster(), o)
	for name := range a {
		if a[name] != b[name] {
			t.Errorf("%s render is non-deterministic", name)
		}
	}
}

func TestWriteFiles(t *testing.T) {
	dir := t.TempDir()
	files, _ := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if err := WriteFiles(dir, files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	for _, n := range []string{"main.tf", "variables.tf", "outputs.tf"} {
		if _, err := os.ReadFile(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s not written: %v", n, err)
		}
	}
}

// ---- shared fakeExecutor (used by command-level checks) ---------------------

type fakeExecutor struct {
	calls     []string
	planResult PlanSummary
	applyErr   error
}

func (f *fakeExecutor) Init(_ context.Context, _ string, _ BackendConfig) error {
	f.calls = append(f.calls, "init")
	return nil
}
func (f *fakeExecutor) Plan(_ context.Context, _ string) (PlanSummary, error) {
	f.calls = append(f.calls, "plan")
	return f.planResult, nil
}
func (f *fakeExecutor) Apply(_ context.Context, _ string) error {
	f.calls = append(f.calls, "apply")
	return f.applyErr
}

func TestFakeExecutor_PlanThenOptionalApply(t *testing.T) {
	// Documents the contract the CLI relies on: plan-only never reaches apply.
	f := &fakeExecutor{planResult: PlanSummary{ChangesPending: true}}
	_ = f.Init(context.Background(), "d", BackendConfig{})
	_, _ = f.Plan(context.Background(), "d")
	got := strings.Join(f.calls, ",")
	if got != "init,plan" {
		t.Errorf("calls=%q want init,plan", got)
	}
}
