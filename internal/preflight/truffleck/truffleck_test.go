package truffleck

import (
	"strings"
	"testing"

	trufflequotas "github.com/spore-host/truffle/pkg/quotas"
)

// verdict maps the rung purchase model onto truffle's spot/on-demand quota
// lookup. CanLaunch reads only its arguments (not client state), so a zero-value
// client is sufficient to exercise the mapping without AWS.
func TestVerdict_SpotVsOnDemand(t *testing.T) {
	q := &trufflequotas.Client{}
	famP := trufflequotas.GetQuotaFamily("p5.48xlarge")
	info := &trufflequotas.QuotaInfo{
		OnDemand: map[trufflequotas.QuotaFamily]int32{famP: 192}, // exactly fits one p5.48xlarge
		Spot:     map[trufflequotas.QuotaFamily]int32{famP: 0},   // no spot headroom
		Usage:    map[trufflequotas.QuotaFamily]int32{},
	}

	// On-demand: 192 vCPU within the 192 quota → OK, with a positive detail.
	ok, detail := verdict(q, "p5.48xlarge", 192, "ondemand", info)
	if !ok {
		t.Fatalf("on-demand should pass: %s", detail)
	}
	if !strings.Contains(detail, "on-demand") || !strings.Contains(detail, "within") {
		t.Errorf("on-demand detail not legible: %q", detail)
	}

	// Spot: same type, but the spot family quota is 0 → FAIL, truffle's reason.
	ok, detail = verdict(q, "p5.48xlarge", 192, "spot", info)
	if ok {
		t.Fatalf("spot should fail (0 spot quota): %s", detail)
	}
	if detail == "" {
		t.Error("a failing verdict must carry truffle's reason, not an empty detail")
	}
}

// Over-quota on-demand fails and the detail names the shortfall (truffle's text).
func TestVerdict_OverQuota(t *testing.T) {
	q := &trufflequotas.Client{}
	famP := trufflequotas.GetQuotaFamily("p5.48xlarge")
	info := &trufflequotas.QuotaInfo{
		OnDemand: map[trufflequotas.QuotaFamily]int32{famP: 96}, // too small for 192
		Usage:    map[trufflequotas.QuotaFamily]int32{},
	}
	ok, detail := verdict(q, "p5.48xlarge", 192, "ondemand", info)
	if ok {
		t.Fatalf("192 vCPU should not fit a 96 quota: %s", detail)
	}
	if detail == "" {
		t.Error("over-quota verdict must carry a reason")
	}
}

// Reserved rungs skip the vCPU quota entirely — verified at the ServiceQuotaOK
// boundary (no AWS reached: it returns before getQuotas).
func TestServiceQuotaOK_ReservedSkips(t *testing.T) {
	c := &Checker{} // no AWS clients needed: reserved short-circuits first
	ok, detail, err := c.ServiceQuotaOK(t.Context(), "p5.48xlarge", "us-east-1a", "reserved")
	if err != nil {
		t.Fatalf("reserved must not error: %v", err)
	}
	if !ok {
		t.Errorf("reserved capacity should pass the quota check: %q", detail)
	}
	if !strings.Contains(detail, "reserved") {
		t.Errorf("reserved detail should explain the skip: %q", detail)
	}
}
