module github.com/queuezero/queuezero

go 1.26

// Direct dependencies pinned for the scaffold.
require github.com/spf13/cobra v1.8.1

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)

// --- queuezero ecosystem -----------------------------------------------------
// These are integrated as the build progresses; pin exact module paths and
// versions in CLAUDE.md once confirmed. They are deliberately NOT required yet
// so the scaffold builds clean:
//
//   ASBX  aws-slurm-burst          — Slurm resume/suspend bridge (embed as lib)
//   ASBA  aws-slurm-burst-advisor  — capacity fallback-chain advisor
//   ASBB  aws-slurm-burst-budget   — spend-rate admission control
//   spore.host  github.com/spore-host/spore-host — fleet lifecycle + truffle
//   aws-sdk-go-v2 — added with the substrate/aws implementation (phase 1)
