package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractArtifact unpacks a downloaded artifact into destDir according
// to the aqua format. Archive extraction shells out to the system tar
// and unzip, which the image bakes in (tar, xz-utils, unzip) — no Go
// decompression dependencies. destDir must exist and be empty.
func extractArtifact(ctx context.Context, artifact, format, destDir, binName string) error {
	switch format {
	case "tar.gz", "tgz":
		return runQuiet(ctx, "tar", "-xzf", artifact, "-C", destDir)
	case "tar.xz", "txz":
		return runQuiet(ctx, "tar", "-xJf", artifact, "-C", destDir)
	case "tar.bz2", "tbz2":
		return runQuiet(ctx, "tar", "-xjf", artifact, "-C", destDir)
	case "tar.zst":
		return runQuiet(ctx, "tar", "--zstd", "-xf", artifact, "-C", destDir)
	case "tar":
		return runQuiet(ctx, "tar", "-xf", artifact, "-C", destDir)
	case "zip":
		return runQuiet(ctx, "unzip", "-q", artifact, "-d", destDir)
	case "gz":
		// Single gzip-compressed binary: decompress to the bin name.
		return decompressTo(ctx, filepath.Join(destDir, binName), "gunzip", "-c", artifact)
	case "xz":
		return decompressTo(ctx, filepath.Join(destDir, binName), "xz", "-dc", artifact)
	case "raw", "":
		// Plain binary: move into place under the bin name.
		out := filepath.Join(destDir, binName)
		if err := os.Rename(artifact, out); err != nil {
			// Cross-device fallback: copy.
			data, rerr := os.ReadFile(artifact)
			if rerr != nil {
				return err
			}
			if werr := os.WriteFile(out, data, 0o755); werr != nil {
				return werr
			}
		}
		return os.Chmod(out, 0o755)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

// decompressTo runs a decompressor with its stdout wired straight to
// the output file — no shell, no quoting concerns.
func decompressTo(ctx context.Context, out string, name string, args ...string) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if cerr := f.Close(); runErr == nil {
		runErr = cerr
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return fmt.Errorf("%s failed: %w (%s)", name, runErr, msg)
	}
	return nil
}

// runQuiet runs a command, returning combined output only on failure.
func runQuiet(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return fmt.Errorf("%s failed: %w (%s)", name, err, msg)
	}
	return nil
}

// mustRel returns target relative to base, or a string that safeJoin
// will reject when target is not under base.
func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return ".." // guaranteed rejection
	}
	return rel
}

// safeJoin joins base and rel, rejecting any path that escapes base
// (absolute rel or .. traversal). Guards files[].src from the registry
// against writing outside the tool's install dir.
func safeJoin(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path %q not allowed", rel)
	}
	joined := filepath.Join(base, rel)
	cleanBase := filepath.Clean(base) + string(os.PathSeparator)
	if !strings.HasPrefix(joined, cleanBase) {
		return "", fmt.Errorf("path %q escapes install dir", rel)
	}
	return joined, nil
}
