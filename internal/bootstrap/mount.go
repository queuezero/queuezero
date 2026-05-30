package bootstrap

import "strings"

// Mount is one shared-storage mount delivered to a node: the filesystem DNS name
// (e.g. an EFS mount-target DNS) and the local path it should be mounted at. The
// queuezero side delivers these to the node; the operator's bootstrap.sh reads
// them (from /etc/q0/mounts) and performs the actual mount.
type Mount struct {
	DNS  string
	Path string
}

// FormatMountSpec encodes mounts as "dns:path,dns:path" — the Q0_MOUNT_SPEC
// value. Entries with an empty DNS or Path are skipped (nothing to mount).
func FormatMountSpec(mounts []Mount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		if m.DNS == "" || m.Path == "" {
			continue
		}
		parts = append(parts, m.DNS+":"+m.Path)
	}
	return strings.Join(parts, ",")
}

// ParseMountSpec is the inverse of FormatMountSpec. It tolerates empty input,
// surrounding whitespace, and skips malformed entries (no colon, empty halves).
// The path may itself contain colons only if... it cannot — a path with a colon
// is rejected; mount paths are absolute POSIX paths without colons in practice,
// so split on the FIRST colon (DNS never contains one).
func ParseMountSpec(spec string) []Mount {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var mounts []Mount
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		i := strings.IndexByte(entry, ':')
		if i <= 0 || i == len(entry)-1 {
			continue // no colon, or empty dns/path half
		}
		mounts = append(mounts, Mount{DNS: entry[:i], Path: entry[i+1:]})
	}
	return mounts
}

// MountPaths returns the comma-separated mount paths — the Q0_MOUNT_PATHS value
// that q0-spored's MountProbe consumes. Skips empty paths.
func MountPaths(mounts []Mount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		if m.Path != "" {
			parts = append(parts, m.Path)
		}
	}
	return strings.Join(parts, ",")
}
