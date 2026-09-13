package harness

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

const artifactDir = ".golum/artifacts"

func (d *Driver) spillBound(ctx context.Context, result tool.Result, toolCallID string) tool.Result {
	return spillAndBound(ctx, d.deps.Env, result, d.cfg.MaxToolResultBytes, toolCallID)
}

// spillAndBound writes oversized output to a workspace artifact, then returns a
// preview that fits in max bytes including a footer pointing at the file.
func spillAndBound(ctx context.Context, env execenv.ExecutionEnv, result tool.Result, max int, toolCallID string) tool.Result {
	if result.OutputBytes == 0 && result.Content != "" && !result.Truncated {
		result.OutputBytes = len(result.Content)
	}
	if max <= 0 || len(result.Content) <= max {
		return result
	}
	full := result.Content
	path := artifactRelPath(toolCallID)
	if env != nil && path != "" {
		if err := env.WriteFile(ctx, path, full); err == nil {
			result.ArtifactPath = path
		}
	}
	footer := artifactFooter(result.ArtifactPath, result.OutputBytes)
	previewMax := max - len(footer)
	if previewMax < 8 {
		footer = ""
		previewMax = max
	}
	result.Content = truncateResult(full, previewMax) + footer
	if len(result.Content) > max {
		result.Content = truncateResult(result.Content, max)
	}
	result.Truncated = true
	return result
}

func artifactRelPath(id string) string {
	if id == "" {
		id = "unknown"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "unknown"
	}
	return artifactDir + "/" + name + ".txt"
}

func artifactFooter(path string, bytes int) string {
	if path == "" {
		return fmt.Sprintf("\n\nFull output (%d bytes) was truncated.\n", bytes)
	}
	return fmt.Sprintf("\n\nFull output (%d bytes) saved to %s. Use read_file to inspect.\n", bytes, path)
}
