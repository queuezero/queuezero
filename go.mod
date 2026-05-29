module github.com/queuezero/queuezero

go 1.26

// Direct dependencies pinned for the scaffold.
require github.com/spf13/cobra v1.8.1

require (
	github.com/aws/aws-sdk-go-v2 v1.41.9
	github.com/aws/aws-sdk-go-v2/config v1.32.19
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.25
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.57.6
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.303.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.102.2
	github.com/aws/smithy-go v1.26.0
	golang.org/x/sync v0.20.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.11 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.18 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.26 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.18 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.1.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.18 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.42.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
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
