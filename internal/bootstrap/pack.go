package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Entrypoint is the script the userdata shim execs after unpacking the
// script-set (see shim.go: exec /opt/q0/bootstrap/bootstrap.sh). A script-set
// without it would boot into nothing, so Pack requires it.
const Entrypoint = "bootstrap.sh"

// Pack writes a deterministic .tar.gz of the script-set directory dir to w and
// returns the lowercase-hex sha256 of the COMPRESSED bytes — the same digest the
// node re-computes with `sha256sum -c` and the same one that goes in the
// content-addressed key (ScriptKey). Hashing and writing are a single pass via
// an io.MultiWriter.
//
// Determinism is required: content-addressing means the same tree must always
// produce the same digest, so entries are sorted and mtime/uid/gid/uname are
// zeroed. dir must contain Entrypoint (bootstrap.sh).
func Pack(dir string, w io.Writer) (string, error) {
	entrypoint := filepath.Join(dir, Entrypoint)
	if fi, err := os.Stat(entrypoint); err != nil || fi.IsDir() {
		return "", fmt.Errorf("bootstrap: script-set %q must contain an executable %s entrypoint", dir, Entrypoint)
	}

	files, err := collectFiles(dir)
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(w, hasher))
	tw := tar.NewWriter(gz)

	for _, rel := range files {
		if err := addFile(tw, dir, rel); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("bootstrap: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("bootstrap: close gzip: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ScriptKey is the content-addressed object key for a digest. It MUST match the
// consumer's parser (substrate/aws.sha256FromKey): scripts/<sha256>.tar.gz.
func ScriptKey(sha256hex string) string {
	return "scripts/" + sha256hex + ".tar.gz"
}

// collectFiles returns the regular files under dir, as sorted relative paths.
func collectFiles(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("bootstrap: %q is not a regular file (symlinks/devices unsupported in a script-set)", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: walk %q: %w", dir, err)
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("bootstrap: script-set %q is empty", dir)
	}
	sort.Strings(rels)
	return rels, nil
}

// addFile writes one regular file into the tar with a normalized, deterministic
// header (zero times/ids, preserve only the executable bit).
func addFile(tw *tar.Writer, dir, rel string) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("bootstrap: stat %q: %w", full, err)
	}
	mode := int64(0o644)
	if fi.Mode().Perm()&0o100 != 0 {
		mode = 0o755 // preserve executability (bootstrap.sh must run)
	}
	hdr := &tar.Header{
		Name:   rel,
		Mode:   mode,
		Size:   fi.Size(),
		Typeflag: tar.TypeReg,
		// All other fields (ModTime, Uid, Gid, Uname, Gname) left zero for
		// reproducibility.
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("bootstrap: tar header %q: %w", rel, err)
	}
	f, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("bootstrap: open %q: %w", full, err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("bootstrap: copy %q: %w", rel, err)
	}
	return nil
}
