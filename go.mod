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
//   ASBX  aws-slurm-burst          — Slurm resume/suspend bridge (embed as lib)
//   ASBA  aws-slurm-burst-advisor  — capacity fallback-chain advisor
//   ASBB  aws-slurm-burst-budget   — spend-rate admission control
//   spore.host  github.com/spore-host/spore-host — fleet lifecycle + truffle
//   aws-sdk-go-v2 — added with the substrate/aws implementation (phase 1)
