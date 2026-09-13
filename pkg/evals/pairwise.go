package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

// SeedEnv names the environment variable that fixes randomized presentation
// order, so a comparative run can be reproduced exactly.
const SeedEnv = "GOLUM_EVAL_SEED"

// Pairwise verdicts.
const (
	VerdictA   = "a"
	VerdictB   = "b"
	VerdictTie = "tie"
)

// Candidate is one side of a pairwise comparison.
type Candidate struct {
	Name   string
	Result *Result
}

// PairwiseVerdict records both the judgment and the order it was made under,
// so a suspicious result can be re-read rather than merely trusted.
type PairwiseVerdict struct {
	// Winner is VerdictA, VerdictB, or VerdictTie, always in terms of the
	// candidates as the caller passed them, never as they were shown.
	Winner string `json:"winner"`
	// PresentedFirst is the name of the candidate shown as "Response 1".
	PresentedFirst string `json:"presented_first"`
	Rationale      string `json:"rationale,omitempty"`
}

// PairwiseJudge compares two runs of the same task.
type PairwiseJudge func(ctx context.Context, a, b Candidate) (PairwiseVerdict, error)

// NewSeededRand returns a generator seeded from GOLUM_EVAL_SEED when set and
// from label otherwise. Deriving the fallback from a label rather than the
// clock keeps a suite reproducible by default: the same comparison in the same
// order gets the same presentation sequence on every run.
func NewSeededRand(label string) *rand.Rand {
	if raw := os.Getenv(SeedEnv); raw != "" {
		if seed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return rand.New(rand.NewSource(seed))
		}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(label))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}

// SeedLabel reports the seed in effect, for the report header.
func SeedLabel() string {
	if raw := os.Getenv(SeedEnv); raw != "" {
		return raw
	}
	return "derived"
}

// NewPairwiseJudge builds a judge that shows two runs to a model and asks
// which better satisfies the rubric.
//
// Presentation order is randomized per comparison because judge models
// systematically favour one position, and a fixed order silently converts that
// bias into a result. The verdict is translated back to the caller's ordering
// before it is returned, and PairwiseTally separately reports how often the
// first-shown candidate won so the bias stays visible.
func NewPairwiseJudge(client *llm.Client, model, rubric string, rng *rand.Rand) PairwiseJudge {
	return func(ctx context.Context, a, b Candidate) (PairwiseVerdict, error) {
		if client == nil {
			return PairwiseVerdict{}, fmt.Errorf("pairwise judge: nil client")
		}
		if a.Result == nil || b.Result == nil {
			return PairwiseVerdict{}, fmt.Errorf("pairwise judge: nil result")
		}
		swapped := rng != nil && rng.Intn(2) == 1
		first, second := a, b
		if swapped {
			first, second = b, a
		}

		system := `You are an eval judge comparing two agent runs on the same task.
Reply with ONLY a JSON object: {"winner": "1" | "2" | "tie", "rationale": "short reason"}.
Judge only against the rubric. Ignore response length, formatting, and confidence of tone.
Answer "tie" when neither run is clearly better.`
		user := fmt.Sprintf("Rubric:\n%s\n\nTask input:\n%s\n\n"+
			"=== Response 1 ===\nTranscript:\n%s\nFinal output:\n%s\n\n"+
			"=== Response 2 ===\nTranscript:\n%s\nFinal output:\n%s\n",
			rubric, first.Result.Input,
			compactTranscript(first.Result), first.Result.Output,
			compactTranscript(second.Result), second.Result.Output)

		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: system},
			{Role: openai.ChatMessageRoleUser, Content: user},
		}
		var content strings.Builder
		for ev := range client.ChatCompletion(ctx, msgs, llm.ChatCompletionOptions{
			Model: model, Stream: false, MaxRetries: 2,
		}) {
			switch ev.Type {
			case llm.EventTypeContentDelta:
				content.WriteString(ev.Content)
			case llm.EventTypeError:
				if ev.Error != nil {
					return PairwiseVerdict{}, fmt.Errorf("pairwise judge: %w", ev.Error)
				}
			}
		}
		position, rationale, err := parsePairwiseJSON(content.String())
		if err != nil {
			return PairwiseVerdict{}, err
		}
		return PairwiseVerdict{
			Winner:         winnerFromPosition(position, swapped),
			PresentedFirst: first.Name,
			Rationale:      rationale,
		}, nil
	}
}

// winnerFromPosition maps a positional verdict back onto the caller's a/b
// ordering. Getting this wrong would invert every swapped comparison, so it is
// kept as its own function with its own test.
func winnerFromPosition(position string, swapped bool) string {
	switch position {
	case "1":
		if swapped {
			return VerdictB
		}
		return VerdictA
	case "2":
		if swapped {
			return VerdictA
		}
		return VerdictB
	default:
		return VerdictTie
	}
}

func parsePairwiseJSON(raw string) (position, rationale string, err error) {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return "", "", fmt.Errorf("pairwise judge: no JSON object in response: %q", truncate(raw, 200))
	}
	var parsed struct {
		Winner    string `json:"winner"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return "", "", fmt.Errorf("pairwise judge: parse %q: %w", truncate(obj, 200), err)
	}
	switch w := strings.TrimSpace(strings.ToLower(parsed.Winner)); w {
	case "1", "response 1", "a":
		return "1", parsed.Rationale, nil
	case "2", "response 2", "b":
		return "2", parsed.Rationale, nil
	default:
		return "tie", parsed.Rationale, nil
	}
}

// PairwiseTally aggregates verdicts for one comparison.
type PairwiseTally struct {
	EvalSet string `json:"eval_set"`
	AName   string `json:"a_name"`
	BName   string `json:"b_name"`
	AWins   int    `json:"a_wins"`
	BWins   int    `json:"b_wins"`
	Ties    int    `json:"ties"`
	N       int    `json:"n"`
	// FirstPositionWinRate is the share of decisive verdicts won by whichever
	// candidate happened to be shown first. Around 0.5 means order did not
	// matter; far from it means the judge is grading position as much as
	// content, and the comparison should not be trusted.
	FirstPositionWinRate float64  `json:"first_position_win_rate"`
	Warnings             []string `json:"warnings,omitempty"`
}

// TallyPairwise counts verdicts by candidate identity rather than by position.
func TallyPairwise(evalSet, aName, bName string, verdicts []PairwiseVerdict) PairwiseTally {
	tally := PairwiseTally{EvalSet: evalSet, AName: aName, BName: bName, N: len(verdicts)}
	firstWins, decisive := 0, 0
	for _, v := range verdicts {
		switch v.Winner {
		case VerdictA:
			tally.AWins++
		case VerdictB:
			tally.BWins++
		default:
			tally.Ties++
			continue
		}
		decisive++
		winnerName := aName
		if v.Winner == VerdictB {
			winnerName = bName
		}
		if v.PresentedFirst == winnerName {
			firstWins++
		}
	}
	if decisive > 0 {
		tally.FirstPositionWinRate = float64(firstWins) / float64(decisive)
	}
	if w := MinSamplesWarning(tally.N); w != "" {
		tally.Warnings = append(tally.Warnings, w)
	}
	if decisive >= MinRecommendedSamples &&
		(tally.FirstPositionWinRate >= 0.8 || tally.FirstPositionWinRate <= 0.2) {
		tally.Warnings = append(tally.Warnings, fmt.Sprintf(
			"position bias: the first-shown candidate won %.0f%% of decisive comparisons",
			tally.FirstPositionWinRate*100))
	}
	return tally
}

func (p PairwiseTally) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pairwise eval_set=%q n=%d\n", p.EvalSet, p.N)
	fmt.Fprintf(&b, "  %s wins=%d  %s wins=%d  ties=%d\n", p.AName, p.AWins, p.BName, p.BWins, p.Ties)
	fmt.Fprintf(&b, "  first-position win rate=%.2f\n", p.FirstPositionWinRate)
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	return b.String()
}
