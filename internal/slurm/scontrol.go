package slurm

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Scontrol is the Slurm CLI seam: hostlist expansion and node-state writeback.
// It is an interface so the resume/suspend logic is testable without a real
// Slurm controller.
//
// Shelling `scontrol` does NOT violate the "link, don't shell" rule (CLAUDE.md
// non-negotiable #7): that rule governs spore.host PROVIDER tools, where a
// classified error must arrive as a Go value rather than parsed stderr.
// `scontrol` is Slurm's own CLI and the documented runtime seam (ARCHITECTURE
// §11) — it carries no provider-error classification, only hostlist expansion
// and node-state writes. Keeping it behind this interface makes it the single
// shell-out in the resume path.
type Scontrol interface {
	// ShowHostnames expands a Slurm hostlist (e.g. "gpu-[001-004]") into the
	// individual node names.
	ShowHostnames(ctx context.Context, hostlist string) ([]string, error)
	// UpdateNode sets a node's state with a reason, e.g.
	//   scontrol update nodename=gpu-042 state=down reason="ICE on p5.48xlarge"
	UpdateNode(ctx context.Context, node, state, reason string) error
}

// execScontrol shells the real `scontrol` binary. When the binary is not on
// PATH (dev/CI without Slurm), ShowHostnames falls back to an in-process
// bracket-range expansion so the resume path is still exercisable; UpdateNode
// becomes a no-op in that case (there is no controller to write to).
type execScontrol struct {
	bin       string // resolved path, or "" when scontrol is absent
	available bool
}

// NewScontrol resolves `scontrol` on PATH and returns an execScontrol. Absence
// is not an error: ShowHostnames uses the in-process fallback and UpdateNode
// no-ops, which keeps local development and CI working.
func NewScontrol() Scontrol {
	path, err := exec.LookPath("scontrol")
	if err != nil {
		return &execScontrol{available: false}
	}
	return &execScontrol{bin: path, available: true}
}

func (s *execScontrol) ShowHostnames(ctx context.Context, hostlist string) ([]string, error) {
	if !s.available {
		return expandHostlist(hostlist)
	}
	out, err := exec.CommandContext(ctx, s.bin, "show", "hostnames", hostlist).Output()
	if err != nil {
		return nil, fmt.Errorf("slurm: scontrol show hostnames %q: %w", hostlist, err)
	}
	var nodes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

func (s *execScontrol) UpdateNode(ctx context.Context, node, state, reason string) error {
	if !s.available {
		// No controller to write to in the local fallback; nothing to do.
		return nil
	}
	args := []string{"update", "nodename=" + node, "state=" + state}
	if reason != "" {
		args = append(args, "reason="+reason)
	}
	if err := exec.CommandContext(ctx, s.bin, args...).Run(); err != nil {
		return fmt.Errorf("slurm: scontrol update nodename=%s state=%s: %w", node, state, err)
	}
	return nil
}

// expandHostlist expands a single Slurm bracket range like "gpu-[001-004]" into
// ["gpu-001","gpu-002","gpu-003","gpu-004"], preserving zero-padding. A name
// with no bracket is returned as-is. This is the dev/CI fallback only; the real
// `scontrol show hostnames` handles the full grammar (multiple ranges, commas).
func expandHostlist(hostlist string) ([]string, error) {
	hostlist = strings.TrimSpace(hostlist)
	open := strings.IndexByte(hostlist, '[')
	if open < 0 {
		// Possibly a comma-separated plain list with no ranges.
		var out []string
		for _, n := range strings.Split(hostlist, ",") {
			if n = strings.TrimSpace(n); n != "" {
				out = append(out, n)
			}
		}
		return out, nil
	}
	close := strings.IndexByte(hostlist, ']')
	if close < 0 || close < open {
		return nil, fmt.Errorf("slurm: malformed hostlist %q", hostlist)
	}
	prefix := hostlist[:open]
	suffix := hostlist[close+1:]
	rangeSpec := hostlist[open+1 : close]

	var out []string
	for _, part := range strings.Split(rangeSpec, ",") {
		part = strings.TrimSpace(part)
		lo, hi, width, err := parseRange(part)
		if err != nil {
			return nil, err
		}
		for i := lo; i <= hi; i++ {
			out = append(out, fmt.Sprintf("%s%0*d%s", prefix, width, i, suffix))
		}
	}
	return out, nil
}

// parseRange parses "001-004" -> (1,4,3) or "007" -> (7,7,3). width is the
// zero-padded field width taken from the low bound's text length.
func parseRange(part string) (lo, hi, width int, err error) {
	if dash := strings.IndexByte(part, '-'); dash >= 0 {
		loStr, hiStr := part[:dash], part[dash+1:]
		lo, err = strconv.Atoi(loStr)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("slurm: bad range low %q: %w", loStr, err)
		}
		hi, err = strconv.Atoi(hiStr)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("slurm: bad range high %q: %w", hiStr, err)
		}
		return lo, hi, len(loStr), nil
	}
	n, err := strconv.Atoi(part)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("slurm: bad index %q: %w", part, err)
	}
	return n, n, len(part), nil
}
