// Package asbb is queuezero's client for ASBB — aws-slurm-burst-budget, the
// spend-rate admission service (ARCHITECTURE §1, §12: the real scheduler is
// spend-rate admission, not Slurm's queue). It satisfies slurm.Admitter so the
// resume path can refuse a launch the project's budget cannot afford, and
// slurm.Reconciler so suspend can close the hold against actuals.
//
// ASBB is a separate, DB-backed HTTP service; its budget state belongs there,
// not linked into the resume binary. So this is a thin HTTP client, not a
// library link.
//
// CONTRACT (github.com/scttfrdmn/aws-slurm-burst-budget):
//
//	POST {endpoint}/api/v1/budget/admit       (fleet-shaped admission, ASBB #6)
//	  req:  {account, partition, region, instance_type, capacity_model, count}
//	  resp: {available, estimated_cost ($/hr), budget_remaining, transaction_id, ...}
//
//	POST {endpoint}/api/v1/budget/reconcile   (close a hold, ASBB #7)
//	  req:  {job_id, actual_cost, transaction_id}
//
// The request is FLEET-shaped: a Slurm ResumeProgram has no job walltime, so the
// gate prices from {instance_type, count, region} and holds against the per-hour
// rate. The returned transaction_id is persisted (slurm.HoldStore) and closed at
// suspend. These endpoints are tracked as ASBB #6/#7; the JSON shapes mirror
// ASBB's pkg/api types.
package asbb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/queuezero/queuezero/internal/slurm"
)

// Client calls an ASBB budget-check endpoint over HTTP.
type Client struct {
	endpoint string
	http     *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the default HTTP client (timeout, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient returns a Client for the ASBB service at endpoint (e.g.
// "http://budget.internal:8080"). The path is appended by Admit.
func NewClient(endpoint string, opts ...Option) *Client {
	c := &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: 5 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// fleetAdmissionRequest mirrors ASBB's pkg/api.FleetAdmissionRequest JSON.
// Defined here (not imported) because the JSON wire contract is what binds us;
// importing ASBB's module would pull in a DB-backed service. Unlike the old
// job-shaped /budget/check, this carries no cpus/walltime placeholders — resume
// is fleet-shaped, and ASBB prices it from {instance_type, count, region}.
type fleetAdmissionRequest struct {
	Account       string `json:"account"`
	Partition     string `json:"partition"`
	Region        string `json:"region"`
	InstanceType  string `json:"instance_type"`
	CapacityModel string `json:"capacity_model,omitempty"`
	Count         int    `json:"count"`
}

// budgetCheckResponse mirrors ASBB's pkg/api.BudgetCheckResponse JSON. For the
// fleet path estimated_cost is a per-hour rate and transaction_id is the placed
// hold.
type budgetCheckResponse struct {
	Available       bool    `json:"available"`
	EstimatedCost   float64 `json:"estimated_cost"`
	BudgetRemaining float64 `json:"budget_remaining"`
	TransactionID   string  `json:"transaction_id"`
	Message         string  `json:"message"`
	Recommendation  string  `json:"recommendation"`
}

// jobReconcileRequest mirrors ASBB's pkg/api.JobReconcileRequest JSON.
type jobReconcileRequest struct {
	JobID         string  `json:"job_id"`
	ActualCost    float64 `json:"actual_cost"`
	TransactionID string  `json:"transaction_id"`
}

// Admit maps the fleet-shaped slurm.AdmissionRequest onto ASBB's fleet admission
// endpoint and interprets the response. A non-2xx or transport error is returned
// as an error; the caller's fail mode (graceful/strict) decides what that means.
func (c *Client) Admit(ctx context.Context, req slurm.AdmissionRequest) (slurm.AdmissionResult, error) {
	body, err := json.Marshal(fleetAdmissionRequest{
		Account:       req.Account,
		Partition:     req.Partition,
		Region:        req.Region,
		InstanceType:  req.InstanceType,
		CapacityModel: req.CapacityModel,
		Count:         req.Count,
	})
	if err != nil {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: marshal request: %w", err)
	}

	var br budgetCheckResponse
	if err := c.postJSON(ctx, "/api/v1/budget/admit", body, &br); err != nil {
		return slurm.AdmissionResult{}, err
	}

	return slurm.AdmissionResult{
		Allowed:         br.Available,
		Reason:          refusalReason(br),
		EstimatedCost:   br.EstimatedCost,
		BudgetRemaining: br.BudgetRemaining,
		TransactionID:   br.TransactionID,
	}, nil
}

// Reconcile closes a resume-time hold against actual cost via ASBB's reconcile
// endpoint. It satisfies slurm.Reconciler, so Suspend can convert the hold to a
// charge and refund the variance at teardown.
func (c *Client) Reconcile(ctx context.Context, req slurm.ReconcileRequest) error {
	body, err := json.Marshal(jobReconcileRequest{
		JobID:         req.JobID,
		ActualCost:    req.ActualCost,
		TransactionID: req.TransactionID,
	})
	if err != nil {
		return fmt.Errorf("asbb: marshal reconcile: %w", err)
	}
	return c.postJSON(ctx, "/api/v1/budget/reconcile", body, nil)
}

// postJSON POSTs a JSON body to an ASBB path and optionally decodes the JSON
// response into out (nil to discard). A non-2xx or transport error is returned.
func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	url := c.endpoint + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("asbb: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("asbb: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("asbb: %s returned %s", url, resp.Status)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("asbb: decode response: %w", err)
	}
	return nil
}

// refusalReason builds a human-readable reason from the response, preferring the
// service's message, then its recommendation, with the budget numbers appended.
func refusalReason(br budgetCheckResponse) string {
	msg := br.Message
	if msg == "" {
		msg = br.Recommendation
	}
	if msg == "" {
		msg = "budget exhausted"
	}
	return fmt.Sprintf("%s (estimated $%.2f/hr, remaining $%.2f)", msg, br.EstimatedCost, br.BudgetRemaining)
}
