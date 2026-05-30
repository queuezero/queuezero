package asbb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/queuezero/queuezero/internal/slurm"
)

// compile-time: *Client satisfies slurm.Admitter.
var _ slurm.Admitter = (*Client)(nil)

func req() slurm.AdmissionRequest {
	return slurm.AdmissionRequest{
		Cluster: "gauss", Partition: "gpu", Account: "111122223333",
		InstanceType: "p5.48xlarge", CapacityModel: "ondemand", Count: 4, Region: "us-east-1",
	}
}

func TestAdmit_Allowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request carries the fleet shape.
		var got budgetCheckRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Account != "111122223333" || got.Partition != "gpu" || got.Nodes != 4 {
			t.Errorf("request body wrong: %+v", got)
		}
		if r.URL.Path != "/api/v1/budget/check" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(budgetCheckResponse{Available: true, EstimatedCost: 12.5, BudgetRemaining: 500})
	}))
	defer srv.Close()

	res, err := NewClient(srv.URL).Admit(context.Background(), req())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !res.Allowed {
		t.Error("want allowed")
	}
	if res.EstimatedCost != 12.5 || res.BudgetRemaining != 500 {
		t.Errorf("numbers not mapped: %+v", res)
	}
}

func TestAdmit_Refused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(budgetCheckResponse{
			Available: false, EstimatedCost: 42, BudgetRemaining: 10, Message: "burn rate exceeded",
		})
	}))
	defer srv.Close()

	res, err := NewClient(srv.URL).Admit(context.Background(), req())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if res.Allowed {
		t.Error("want refused")
	}
	if !strings.Contains(res.Reason, "burn rate exceeded") {
		t.Errorf("reason should carry the service message, got %q", res.Reason)
	}
}

func TestAdmit_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).Admit(context.Background(), req())
	if err == nil {
		t.Fatal("a 500 must surface as an error (the caller's fail mode decides)")
	}
}

func TestAdmit_Unreachable(t *testing.T) {
	// A closed server endpoint => transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := NewClient(url).Admit(context.Background(), req()); err == nil {
		t.Fatal("unreachable endpoint should error")
	}
}

// The request body is well-formed JSON the service can read.
func TestAdmit_RequestIsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !json.Valid(b) {
			t.Errorf("request body is not valid JSON: %s", b)
		}
		_ = json.NewEncoder(w).Encode(budgetCheckResponse{Available: true})
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL).Admit(context.Background(), req()); err != nil {
		t.Fatal(err)
	}
}
