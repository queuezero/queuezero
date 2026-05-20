// Package spec defines queuezero's declarative spec files. Each file is a
// composable LAYER with its own content hash and its own `q0 apply <layer>`:
//
//   cluster.yaml     — control account, network (or BYO), the controller pair
//   stack.yaml       — software stack (Strata attested squashfs layers)
//   partitions.yaml  — partitions, partition->account map, fallback chains
//   users.yaml       — identity / accounting
//
// partitions.yaml references a stack.yaml hash; the controller references an
// AMI hash. This lets the software stack roll without recycling the network,
// and partitions roll without touching identity — the composability that
// ParallelCluster's single-graph model cannot express. See ARCHITECTURE §2.
package spec
