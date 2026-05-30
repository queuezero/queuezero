package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

// fakeRegionGetter records whether it was called and returns a canned region/err.
type fakeRegionGetter struct {
	region string
	err    error
	called bool
}

func (f *fakeRegionGetter) GetRegion(_ context.Context, _ *imds.GetRegionInput, _ ...func(*imds.Options)) (*imds.GetRegionOutput, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return &imds.GetRegionOutput{Region: f.region}, nil
}

func TestResolveRegion_EnvWins(t *testing.T) {
	g := &fakeRegionGetter{region: "us-west-2"}
	got, err := resolveRegion(context.Background(), "us-east-1", g)
	if err != nil {
		t.Fatalf("resolveRegion: %v", err)
	}
	if got != "us-east-1" {
		t.Errorf("env should win: got %q, want us-east-1", got)
	}
	if g.called {
		t.Error("IMDS must not be queried when Q0_REGION is set")
	}
}

func TestResolveRegion_IMDSFallback(t *testing.T) {
	g := &fakeRegionGetter{region: "eu-central-1"}
	got, err := resolveRegion(context.Background(), "", g)
	if err != nil {
		t.Fatalf("resolveRegion: %v", err)
	}
	if got != "eu-central-1" {
		t.Errorf("empty env should fall back to IMDS: got %q, want eu-central-1", got)
	}
	if !g.called {
		t.Error("IMDS should be queried when Q0_REGION is empty")
	}
}

func TestResolveRegion_IMDSError(t *testing.T) {
	g := &fakeRegionGetter{err: errors.New("imds unreachable")}
	if _, err := resolveRegion(context.Background(), "", g); err == nil {
		t.Error("expected an error when env is empty and IMDS fails")
	}
}

func TestResolveRegion_IMDSEmpty(t *testing.T) {
	g := &fakeRegionGetter{region: ""}
	if _, err := resolveRegion(context.Background(), "", g); err == nil {
		t.Error("expected an error when env is empty and IMDS returns an empty region")
	}
}
