package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxInstructionBytes caps how much procedural text is folded into the system
// prompt, so an oversized AGENTS.md cannot crowd out the conversation.
const maxInstructionBytes = 32 * 1024

// agentFileNames are the project instruction files recognized at each level.
var agentFileNames = []string{"AGENTS.md", "AGENT.md"}

// LoadProjectInstructions collects project-level procedural memory for the
// workspace rooted at dir.
//
// The system prompt already tells the model that AGENTS.md files apply to the
// tree rooted at their directory and that deeper files win on conflict — but
// nothing ever read them from disk, so that instruction described a capability
// the agent did not have. This closes that gap.
//
// Files are returned outermost-first so that nearer (deeper) instructions
// appear later and therefore take precedence when the model reads top to bottom.
// Discovery uses os directly rather than the workspace ExecutionEnv: this is
// startup configuration the user controls, not model-directed file access, and
// the relevant files often sit at the repository root above the working
// directory, which the workspace sandbox deliberately refuses.
func LoadProjectInstructions(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	var sections []string
	for _, path := range discoverAgentFiles(abs) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		rel := path
		if r, err := filepath.Rel(abs, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		sections = append(sections, fmt.Sprintf("### From %s\n\n%s", rel, body))
	}

	// Project-scoped procedural notes live alongside skills.
	for _, path := range globSorted(filepath.Join(abs, ".golum", "memory", "procedural", "*.md")) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		sections = append(sections, fmt.Sprintf("### From %s\n\n%s",
			filepath.Join(".golum", "memory", "procedural", filepath.Base(path)), body))
	}

	return truncate(strings.Join(sections, "\n\n")), nil
}

// discoverAgentFiles walks from the repository root down to dir, returning the
// instruction files found at each level, outermost first.
func discoverAgentFiles(dir string) []string {
	var chain []string
	cur := dir
	for i := 0; i < 32; i++ {
		chain = append(chain, cur)
		if isRepoRoot(cur) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// chain is innermost-first; emit outermost-first so deeper files win.
	var out []string
	for i := len(chain) - 1; i >= 0; i-- {
		for _, name := range agentFileNames {
			p := filepath.Join(chain[i], name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				out = append(out, p)
				break // one instruction file per directory
			}
		}
	}
	return out
}

// isRepoRoot stops the upward walk at a version-control boundary so we never
// wander into unrelated parent directories.
func isRepoRoot(dir string) bool {
	for _, marker := range []string{".git", ".hg", ".jj"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// LoadUserInstructions reads cross-project instructions from ~/.golum.
func LoadUserInstructions() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".golum")

	var sections []string
	for _, name := range []string{"AGENTS.md", filepath.Join("memory", "user.md")} {
		path := filepath.Join(base, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if body := strings.TrimSpace(string(data)); body != "" {
			sections = append(sections, body)
		}
	}
	for _, path := range globSorted(filepath.Join(base, "memory", "procedural", "*.md")) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if body := strings.TrimSpace(string(data)); body != "" {
			sections = append(sections, body)
		}
	}
	return truncate(strings.Join(sections, "\n\n")), nil
}

func globSorted(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

func truncate(s string) string {
	if len(s) <= maxInstructionBytes {
		return s
	}
	return s[:maxInstructionBytes] + "\n\n[project instructions truncated]"
}
