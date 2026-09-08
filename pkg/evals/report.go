package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TaskReport aggregates repeated runs of one task.
//
// The three pass rates are reported separately and never blended. A single
// number cannot answer the question that actually comes up when an eval
// regresses: did the agent stop producing the right result, stop following the
// rules, or just start explaining itself worse?
type TaskReport struct {
	TaskID     string     `json:"task_id"`
	Version    string     `json:"version,omitempty"`
	Split      Split      `json:"split,omitempty"`
	Difficulty Difficulty `json:"difficulty,omitempty"`
	TaskHash   string     `json:"task_hash,omitempty"`
	Harness    string     `json:"harness,omitempty"`

	N int `json:"n"`
	K int `json:"k"`

	// OutcomePassRate is "produced the correct result".
	OutcomePassRate float64 `json:"outcome_pass_rate"`
	// ProcessPassRate is "used an allowed process".
	ProcessPassRate float64 `json:"process_pass_rate"`
	// ResponsePassRate is "gave a good response", as graded by subjective
	// judges. Informational: it never gates a pass.
	ResponsePassRate float64 `json:"response_pass_rate"`
	// SubjectiveAvgScore keeps the graded nuance the pass rate discards.
	SubjectiveAvgScore float64 `json:"subjective_avg_score"`

	// PassRate counts a run as passing only when both deterministic axes hold.
	PassRate   float64    `json:"pass_rate"`
	PassAtK    float64    `json:"pass_at_k"`
	PassPowerK float64    `json:"pass_power_k"`
	OutcomeCI  [2]float64 `json:"outcome_ci"`

	LatencyMs        Percentiles `json:"latency_ms"`
	MeanTokens       float64     `json:"mean_tokens"`
	MeanToolCalls    float64     `json:"mean_tool_calls"`
	MeanModelCalls   float64     `json:"mean_model_calls"`
	TotalCostUSD     *float64    `json:"total_cost_usd,omitempty"`
	TokensReportedBy int         `json:"runs_reporting_tokens"`

	// Attributions counts failure categories across the repetitions.
	Attributions map[string]int `json:"attributions,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
}

// Report is the whole run's output.
type Report struct {
	GeneratedAt    string          `json:"generated_at"`
	DatasetVersion string          `json:"dataset_version,omitempty"`
	Split          Split           `json:"split,omitempty"`
	Model          string          `json:"model,omitempty"`
	JudgeModel     string          `json:"judge_model,omitempty"`
	Seed           string          `json:"seed,omitempty"`
	Tasks          []TaskReport    `json:"tasks,omitempty"`
	Comparisons    []LiftReport    `json:"comparisons,omitempty"`
	Pairwise       []PairwiseTally `json:"pairwise,omitempty"`
}

// SummarizeTask aggregates repeated runs of one task into a report row. k
// bounds pass@k and pass^k; values above the sample count are clamped, since
// n samples cannot answer a question about more than n draws.
func SummarizeTask(runs []*TaskRun, k int) TaskReport {
	report := TaskReport{N: len(runs)}
	if len(runs) == 0 {
		report.Warnings = append(report.Warnings, "no runs recorded")
		return report
	}

	first := runs[0]
	report.TaskID = first.Task.ID
	report.Version = first.Task.Version
	report.Split = first.Task.SplitOrDefault()
	report.Difficulty = first.Task.Difficulty
	report.TaskHash = TaskHash(first.Task)
	if first.Result != nil {
		report.Harness = first.Result.Harness
	}

	var outcomePasses, processPasses, responsePasses, passes int
	var subjectiveSum float64
	var subjectiveCount int
	var tokens, toolCalls, modelCalls float64
	var latencies []float64
	var cost float64
	costKnown := false
	model := ""
	report.Attributions = map[string]int{}

	for _, run := range runs {
		if run.OutcomePassed {
			outcomePasses++
		}
		if run.ProcessPassed {
			processPasses++
		}
		if run.ResponsePassed {
			responsePasses++
		}
		if run.Passed() {
			passes++
		}
		for _, c := range run.Subjective {
			subjectiveSum += c.Score
			subjectiveCount++
		}
		m := run.Metrics
		if m.Model != "" {
			model = m.Model
		}
		tokens += float64(m.TotalTokens)
		toolCalls += float64(m.ToolCalls)
		modelCalls += float64(m.ModelRequests)
		latencies = append(latencies, float64(m.LatencyMs))
		if m.TokensReported {
			report.TokensReportedBy++
		}
		if m.EstimatedCostUSD != nil {
			cost += *m.EstimatedCostUSD
			costKnown = true
		}
		if run.Attribution != nil {
			report.Attributions[run.Attribution.Kind]++
		}
	}

	n := float64(len(runs))
	report.OutcomePassRate = float64(outcomePasses) / n
	report.ProcessPassRate = float64(processPasses) / n
	report.ResponsePassRate = float64(responsePasses) / n
	report.PassRate = float64(passes) / n
	if subjectiveCount > 0 {
		report.SubjectiveAvgScore = subjectiveSum / float64(subjectiveCount)
	}
	report.MeanTokens = tokens / n
	report.MeanToolCalls = toolCalls / n
	report.MeanModelCalls = modelCalls / n
	report.LatencyMs = Summarize(latencies)
	if costKnown {
		report.TotalCostUSD = &cost
	}

	if k <= 0 {
		k = 1
	}
	if k > len(runs) {
		k = len(runs)
	}
	report.K = k
	if v, err := PassAtK(passes, len(runs), k); err == nil {
		report.PassAtK = v
	}
	if v, err := PassPowerK(passes, len(runs), k); err == nil {
		report.PassPowerK = v
	}
	lo, hi := WilsonInterval(outcomePasses, len(runs), DefaultZ)
	report.OutcomeCI = [2]float64{lo, hi}

	if w := MinSamplesWarning(len(runs)); w != "" {
		report.Warnings = append(report.Warnings, w)
	}
	if report.TokensReportedBy == 0 {
		report.Warnings = append(report.Warnings,
			"provider reported no token usage; token and cost figures are unavailable")
	}
	if !costKnown {
		if w := PricingWarning(model); w != "" {
			report.Warnings = append(report.Warnings, w)
		}
	}
	if len(report.Attributions) == 0 {
		report.Attributions = nil
	}
	return report
}

// WriteJSON writes report.json into dir.
func (r Report) WriteJSON(dir string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), append(raw, '\n'), 0o600)
}

// WriteMarkdown writes report.md into dir.
func (r Report) WriteMarkdown(dir string) error {
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.Markdown()), 0o600)
}

// Markdown renders the report for a human reader.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Golum eval report\n\n")
	writeField(&b, "Generated", r.GeneratedAt)
	writeField(&b, "Dataset", r.DatasetVersion)
	writeField(&b, "Split", string(r.Split))
	writeField(&b, "Model", r.Model)
	writeField(&b, "Judge model", r.JudgeModel)
	writeField(&b, "Seed", r.Seed)

	if len(r.Tasks) > 0 {
		b.WriteString("\n## Tasks\n\n")
		b.WriteString("Outcome = produced the correct result. " +
			"Process = used an allowed process. " +
			"Response = judged a good answer (informational).\n\n")
		b.WriteString("| Task | Diff | N | Outcome | Process | Response | pass@k | pass^k | Outcome 95% CI | p50 ms | Tokens | Tools |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, task := range r.Tasks {
			fmt.Fprintf(&b, "| %s | %s | %d | %.0f%% | %.0f%% | %.0f%% | %.2f | %.2f | %.0f–%.0f%% | %.0f | %.0f | %.1f |\n",
				task.TaskID, task.Difficulty, task.N,
				task.OutcomePassRate*100, task.ProcessPassRate*100, task.ResponsePassRate*100,
				task.PassAtK, task.PassPowerK,
				task.OutcomeCI[0]*100, task.OutcomeCI[1]*100,
				task.LatencyMs.P50, task.MeanTokens, task.MeanToolCalls)
		}
		for _, task := range r.Tasks {
			if len(task.Attributions) == 0 && len(task.Warnings) == 0 && task.TotalCostUSD == nil {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n\n", task.TaskID)
			if task.TaskHash != "" {
				fmt.Fprintf(&b, "- hash: `%s`\n", task.TaskHash)
			}
			if task.TotalCostUSD != nil {
				fmt.Fprintf(&b, "- estimated cost: $%.4f over %d runs\n", *task.TotalCostUSD, task.N)
			}
			for _, kind := range sortedCountKeys(task.Attributions) {
				fmt.Fprintf(&b, "- failures attributed to %s: %d\n", kind, task.Attributions[kind])
			}
			for _, w := range task.Warnings {
				fmt.Fprintf(&b, "- warning: %s\n", w)
			}
		}
	}

	if len(r.Comparisons) > 0 {
		b.WriteString("\n## Comparisons\n\n```\n")
		for _, c := range r.Comparisons {
			b.WriteString(c.String())
		}
		b.WriteString("```\n")
	}
	if len(r.Pairwise) > 0 {
		b.WriteString("\n## Pairwise\n\n```\n")
		for _, p := range r.Pairwise {
			b.WriteString(p.String())
		}
		b.WriteString("```\n")
	}
	return b.String()
}

func writeField(b *strings.Builder, label, value string) {
	if value != "" {
		fmt.Fprintf(b, "- %s: %s\n", label, value)
	}
}

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	reportMu    sync.Mutex
	taskReports []TaskReport
	tallies     []PairwiseTally
)

// RecordTaskReport stores a task summary for aggregation in TestMain.
func RecordTaskReport(r TaskReport) {
	reportMu.Lock()
	defer reportMu.Unlock()
	taskReports = append(taskReports, r)
}

// RecordPairwise stores a pairwise tally for aggregation in TestMain.
func RecordPairwise(p PairwiseTally) {
	reportMu.Lock()
	defer reportMu.Unlock()
	tallies = append(tallies, p)
}

// BuildReport assembles everything recorded during the process into one
// report. Both the pkg/evals and pkg/evals/suite TestMains call it, so a run
// covering either produces the same artifact shape.
func BuildReport(datasetVersion string, split Split, model, judgeModel string) Report {
	reportMu.Lock()
	tasks := append([]TaskReport(nil), taskReports...)
	pairs := append([]PairwiseTally(nil), tallies...)
	reportMu.Unlock()

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	return Report{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		DatasetVersion: datasetVersion,
		Split:          split,
		Model:          model,
		JudgeModel:     judgeModel,
		Seed:           SeedLabel(),
		Tasks:          tasks,
		Comparisons:    SnapshotLifts(),
		Pairwise:       pairs,
	}
}

// HasRecordedResults reports whether anything was recorded, so a TestMain can
// skip writing an empty report.
func HasRecordedResults() bool {
	reportMu.Lock()
	defer reportMu.Unlock()
	return len(taskReports) > 0 || len(tallies) > 0 || len(SnapshotLifts()) > 0
}

// FlushReport writes report.json and report.md into the artifact directory and
// prints a summary to stderr. Both eval TestMains call it, so a run covering
// either package produces the same artifacts in the same shape.
//
// It never fails the process: the tests it summarizes have already finished,
// and losing a report is not worth converting a passing run into a failing
// one. Problems are reported on stderr instead.
func FlushReport(datasetVersion string, getenv func(string) string) {
	if !HasRecordedResults() {
		return
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	split, err := SelectedSplit()
	if err != nil {
		split = SplitDev
	}
	model, _ := ResolveModel("", getenv)
	judgeModel, _ := ResolveJudgeModel("", model, getenv)

	report := BuildReport(datasetVersion, split, model, judgeModel)
	fmt.Fprintf(os.Stderr, "\n%s", report.Markdown())

	dir, err := ArtifactDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "evals: no artifact dir, report not written: %v\n", err)
		return
	}
	if err := report.WriteJSON(dir); err != nil {
		fmt.Fprintf(os.Stderr, "evals: write report.json: %v\n", err)
	}
	if err := report.WriteMarkdown(dir); err != nil {
		fmt.Fprintf(os.Stderr, "evals: write report.md: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "eval report: %s\n", dir)
}
