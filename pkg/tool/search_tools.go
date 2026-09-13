package tool

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

const (
	maxGlobResults = 1000
	maxGrepMatches = 200
)

type globTool struct{}

func (globTool) Name() string { return "glob" }
func (globTool) Description() string {
	return "Find files by glob pattern (supports **). Returns paths sorted by modification time (newest first), capped at 1000."
}
func (globTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern, e.g. **/*.go"},
		"path":    map[string]any{"type": "string", "description": "Subdirectory to search (optional, default workspace root)"},
	}, []string{"pattern"})
}

type globHit struct {
	rel   string
	mtime time.Time
}

func (globTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	pattern, ok := stringArg(args, "pattern")
	if !ok || strings.TrimSpace(pattern) == "" {
		return errResult("missing required argument: pattern"), nil
	}
	root, _ := stringArg(args, "path")
	if strings.TrimSpace(root) == "" {
		root = "."
	}

	var hits []globHit
	err := env.Walk(ctx, root, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		// Match against path relative to search root when root != "."
		matchPath := rel
		if root != "." && root != "" {
			prefix := filepath.ToSlash(filepath.Clean(root)) + "/"
			slashRel := filepath.ToSlash(rel)
			if strings.HasPrefix(slashRel, prefix) {
				matchPath = slashRel[len(prefix):]
			} else {
				matchPath = slashRel
			}
		} else {
			matchPath = filepath.ToSlash(rel)
		}
		ok, err := matchGlob(pattern, matchPath)
		if err != nil || !ok {
			return nil
		}
		info, err := d.Info()
		mtime := time.Time{}
		if err == nil {
			mtime = info.ModTime()
		}
		hits = append(hits, globHit{rel: filepath.ToSlash(rel), mtime: mtime})
		if len(hits) >= maxGlobResults*2 {
			// collect extra then trim after sort
		}
		return nil
	})
	if err != nil {
		return errResult(err.Error()), nil
	}

	sort.Slice(hits, func(i, j int) bool {
		return hits[i].mtime.After(hits[j].mtime)
	})
	if len(hits) > maxGlobResults {
		hits = hits[:maxGlobResults]
	}
	var b strings.Builder
	for _, h := range hits {
		b.WriteString(h.rel)
		b.WriteByte('\n')
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("glob %q → %d files", pattern, len(hits)),
	}, nil
}

// matchGlob supports ** across path segments using path.Match per segment.
func matchGlob(pattern, name string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchGlobSegments(pat, name []string) (bool, error) {
	for {
		if len(pat) == 0 {
			return len(name) == 0, nil
		}
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true, nil
			}
			// Try consuming 0..n name segments
			for i := 0; i <= len(name); i++ {
				ok, err := matchGlobSegments(pat[1:], name[i:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(name) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		pat = pat[1:]
		name = name[1:]
	}
}

type grepTool struct{}

func (grepTool) Name() string { return "grep" }
func (grepTool) Description() string {
	return "Search file contents with a Go regexp. Optional path and include glob. Skips .git, node_modules, vendor, bin, and binary files. Caps at 200 matches."
}
func (grepTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Go regular expression"},
		"path":    map[string]any{"type": "string", "description": "File or directory to search (optional)"},
		"include": map[string]any{"type": "string", "description": "Glob filter for filenames, e.g. *.go (optional)"},
	}, []string{"pattern"})
}

var grepSkipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "vendor": {}, "bin": {},
}

func (grepTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	pattern, ok := stringArg(args, "pattern")
	if !ok || strings.TrimSpace(pattern) == "" {
		return errResult("missing required argument: pattern"), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Sprintf("invalid regexp: %v", err)), nil
	}
	searchPath, _ := stringArg(args, "path")
	if strings.TrimSpace(searchPath) == "" {
		searchPath = "."
	}
	include, _ := stringArg(args, "include")

	// If path is a file, search just that file
	info, err := env.Stat(ctx, searchPath)
	if err == nil && !info.IsDir {
		content, err := env.ReadTextFile(ctx, searchPath)
		if err != nil {
			return errResult(err.Error()), nil
		}
		matches := grepContent(searchPath, content, re, maxGrepMatches)
		return Result{
			Content: strings.Join(matches, "\n"),
			Display: fmt.Sprintf("grep %q → %d matches", pattern, len(matches)),
		}, nil
	}

	var matches []string
	err = env.Walk(ctx, searchPath, func(rel string, d fs.DirEntry) error {
		if len(matches) >= maxGrepMatches {
			return fs.SkipAll
		}
		if d.IsDir() {
			if _, skip := grepSkipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if include != "" {
			base := filepath.Base(rel)
			ok, _ := path.Match(include, base)
			if !ok {
				return nil
			}
		}
		content, err := env.ReadTextFile(ctx, rel)
		if err != nil {
			return nil // skip unreadable / binary / sensitive
		}
		remaining := maxGrepMatches - len(matches)
		found := grepContent(filepath.ToSlash(rel), content, re, remaining)
		matches = append(matches, found...)
		return nil
	})
	if err != nil {
		return errResult(err.Error()), nil
	}
	return Result{
		Content: strings.Join(matches, "\n"),
		Display: fmt.Sprintf("grep %q → %d matches", pattern, len(matches)),
	}, nil
}

func grepContent(file, content string, re *regexp.Regexp, limit int) []string {
	if limit <= 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	for i, line := range lines {
		// Skip truncation markers and binary refuse messages
		if strings.HasPrefix(line, "[truncated:") {
			continue
		}
		if re.MatchString(line) {
			out = append(out, fmt.Sprintf("%s:%d:%s", file, i+1, line))
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
