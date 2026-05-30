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

// compile-time: *Client satisfies both budget-loop seams.
var (
	_ slurm.Admitter   = (*Client)(nil)
	_ slurm.Reconciler = (*Client)(nil)
)

func req() slurm.AdmissionRequest {
	return slurm.AdmissionRequest{
		Cluster: "gauss", Partition: "gpu", Account: "111122223333",
		InstanceType: "p5.48xlarge", CapacityModel: "ondemand", Count: 4, Region: "us-east-1",
	}
}

func TestAdmit_Allowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request carries the fleet shape (no cpus/walltime).
		var got fleetAdmissionRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Account != "111122223333" || got.Partition != "gpu" || got.Count != 4 {
			t.Errorf("request body wrong: %+v", got)
		}
		if got.InstanceType != "p5.48xlarge" || got.Region != "us-east-1" {
			t.Errorf("fleet shape not carried: %+v", got)
		}
		if r.URL.Path != "/api/v1/budget/admit" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(budgetCheckResponse{
			Available: true, EstimatedCost: 12.5, BudgetRemaining: 500, TransactionID: "txn_42",
		})
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
	if res.TransactionID != "txn_42" {
		t.Errorf("transaction id not captured: %q", res.TransactionID)
	}
}

func TestReconcile(t *testing.T) {
	var got jobReconcileRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/budget/reconcile" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	err := NewClient(srv.URL).Reconcile(context.Background(), slurm.ReconcileRequest{
		TransactionID: "txn_42", Account: "111122223333", JobID: "gauss/gpu-001", ActualCost: 3.25,
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.TransactionID != "txn_42" || got.ActualCost != 3.25 || got.JobID != "gauss/gpu-001" {
		t.Errorf("reconcile body wrong: %+v", got)
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
