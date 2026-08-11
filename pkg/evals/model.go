package evals

import "fmt"

// ResolveModel picks the model with the same precedence pi documents for
// PI_PROVIDER/PI_MODEL: explicit harness Options.Model, then GOLUM_EVAL_MODEL,
// then OPENAI_MODEL. getenv should typically be os.Getenv.
func ResolveModel(explicit string, getenv func(string) string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if getenv == nil {
		return "", fmt.Errorf("no model selected: set Options.Model, GOLUM_EVAL_MODEL, or OPENAI_MODEL")
	}
	if m := getenv("GOLUM_EVAL_MODEL"); m != "" {
		return m, nil
	}
	if m := getenv("OPENAI_MODEL"); m != "" {
		return m, nil
	}
	return "", fmt.Errorf("no model selected: set Options.Model, GOLUM_EVAL_MODEL, or OPENAI_MODEL")
}

// ResolveJudgeModel picks the model an LLM judge scores with, kept independent
// of the model under test so a comparative eval isn't graded by the same model
// that produced the output (a model is not a reliable judge of its own work).
// Precedence: explicit, then GOLUM_EVAL_JUDGE_MODEL, then the run model itself —
// falling back to the run model keeps evals runnable with only one model
// configured, but GOLUM_EVAL_JUDGE_MODEL lets any run decouple actor and judge.
func ResolveJudgeModel(explicit string, runModel string, getenv func(string) string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if getenv != nil {
		if m := getenv("GOLUM_EVAL_JUDGE_MODEL"); m != "" {
			return m, nil
		}
	}
	if runModel != "" {
		return runModel, nil
	}
	return "", fmt.Errorf("no judge model selected: set GOLUM_EVAL_JUDGE_MODEL or pass a run model")
}
