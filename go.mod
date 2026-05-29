module github.com/queuezero/queuezero

go 1.26

// Direct dependencies pinned for the scaffold.
require github.com/spf13/cobra v1.8.1

require (
	github.com/aws/aws-sdk-go-v2 v1.41.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.303.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.23 // indirect
	github.com/aws/smithy-go v1.25.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/sync v0.20.0 // indirect
)

// --- queuezero ecosystem -----------------------------------------------------
// These are integrated as the build progresses; pin exact module paths and
// versions in CLAUDE.md once confirmed. They are deliberately NOT required yet
// so the scaffold builds clean:
//
//   ASBX  github.com/scttfrdmn/aws-slurm-burst         v0.4.0 — Slurm resume/suspend bridge (embed as lib)
//   ASBA  github.com/scttfrdmn/aws-slurm-burst-advisor v0.3.0 — capacity fallback-chain advisor
//   ASBB  github.com/scttfrdmn/aws-slurm-burst-budget  v0.2.0 — spend-rate admission control
//   ASBC  aws-slurm-burst-config — config/spec layer; local placeholder ~/src/aws-slurm-burst-config (empty)
//   spore.host  repo github.com/spore-host/spore-host; modules drop the repo segment
//               (spawn=github.com/spore-host/spawn, truffle, lagotto) — fleet lifecycle + truffle
//   aws-sdk-go-v2 — added with the substrate/aws implementation (phase 1)
