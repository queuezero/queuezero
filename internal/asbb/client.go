// Package asbb is queuezero's client for ASBB — aws-slurm-burst-budget, the
// spend-rate admission service (ARCHITECTURE §1, §12: the real scheduler is
// spend-rate admission, not Slurm's queue). It satisfies slurm.Admitter so the
// resume path can refuse a launch the project's budget cannot afford.
//
// ASBB is a separate, DB-backed HTTP service; its budget state belongs there,
// not linked into the resume binary. So this is a thin HTTP client, not a
// library link.
//
// CONTRACT (github.com/scttfrdmn/aws-slurm-burst-budget):
//
//	POST {endpoint}/api/v1/budget/check
//	  req:  {account, partition, nodes, cpus, gpus, memory, wall_time}
//	  resp: {available bool, estimated_cost, budget_remaining, message, ...}
//
// queuezero's admission request is FLEET-shaped (partition/account, instance
// type, node count) — a Slurm ResumeProgram has no job walltime — so the
// job-shaped fields below are best-effort. The durable fix is a resume-shaped
// ASBB endpoint (tracked in the ASBB repo); until then ASBB estimates cost from
// the fleet shape via its own advisor/fallback.
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

// budgetCheckRequest mirrors ASBB's pkg/api.BudgetCheckRequest JSON. Defined
// here (not imported) because ASBB's Go client is a stub and importing its
// module would pull in a DB-backed service; the JSON contract is what binds us.
type budgetCheckRequest struct {
	Account   string `json:"account"`
	Partition string `json:"partition"`
	Nodes     int    `json:"nodes"`
	CPUs      int    `json:"cpus"`
	WallTime  string `json:"wall_time"`
	JobDetails map[string]string `json:"job_details,omitempty"`
}

// budgetCheckResponse mirrors ASBB's pkg/api.BudgetCheckResponse JSON.
type budgetCheckResponse struct {
	Available       bool    `json:"available"`
	EstimatedCost   float64 `json:"estimated_cost"`
	BudgetRemaining float64 `json:"budget_remaining"`
	Message         string  `json:"message"`
	Recommendation  string  `json:"recommendation"`
}

// Admit maps the fleet-shaped slurm.AdmissionRequest onto ASBB's budget-check
// endpoint and interprets the response. A non-2xx or transport error is returned
// as an error; the caller's fail mode (graceful/strict) decides what that means.
func (c *Client) Admit(ctx context.Context, req slurm.AdmissionRequest) (slurm.AdmissionResult, error) {
	body, err := json.Marshal(budgetCheckRequest{
		Account:   req.Account,
		Partition: req.Partition,
		Nodes:     req.Count,
		// Best-effort: resume has no per-job CPU/walltime. ASBB validates these as
		// required and estimates cost from the fleet shape + its advisor/fallback.
		CPUs:     1,
		WallTime: "1:00:00",
		JobDetails: map[string]string{
			"cluster":       req.Cluster,
			"instance_type": req.InstanceType,
			"capacity_model": req.CapacityModel,
			"region":        req.Region,
		},
	})
	if err != nil {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: marshal request: %w", err)
	}

	url := c.endpoint + "/api/v1/budget/check"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: %s returned %s", url, resp.Status)
	}

	var br budgetCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return slurm.AdmissionResult{}, fmt.Errorf("asbb: decode response: %w", err)
	}

	return slurm.AdmissionResult{
		Allowed:         br.Available,
		Reason:          refusalReason(br),
		EstimatedCost:   br.EstimatedCost,
		BudgetRemaining: br.BudgetRemaining,
	}, nil
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
