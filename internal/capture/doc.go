// Package capture is the single introspect->spec backend behind both the
// migration tooling and the bursting stretch goal — they are the same thing.
//
//   q0 import parallelcluster — parse a PC config file; HeadNode->controller,
//     SlurmQueues->partitions.yaml, SharedStorage->storage. Flag what doesn't
//     map; emit a review report.
//
//   q0 capture — introspect a LIVE on-prem cluster: `scontrol show config`,
//     `sinfo`, partition/node specs, users from sacctmgr/LDAP, the software
//     stack from Lmod/Spack/EasyBuild manifests. Emit spec files replicating it.
//
// PC import is just the special case where the "live cluster" is a file. The
// three run modes — greenfield, replicate, burst — are one architecture with
// two parameters: where slurmctld lives, and what already exists.
package capture
