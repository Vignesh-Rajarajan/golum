package prompt

import "fmt"

const (
	BRANCH_SUMMARY_PREFIX     = "<branch_summary>\n"
	BRANCH_SUMMARY_SUFFIX     = "\n</branch_summary>"
	COMPACTION_SUMMARY_PREFIX = "<compaction_summary>\n"
	COMPACTION_SUMMARY_SUFFIX = "\n</compaction_summary>"
)

func WrapBranchSummary(summary string) string {
	return BRANCH_SUMMARY_PREFIX + summary + BRANCH_SUMMARY_SUFFIX
}

func WrapCompactionSummary(summary string) string {
	return COMPACTION_SUMMARY_PREFIX + summary + COMPACTION_SUMMARY_SUFFIX
}

// GetCompressionPrompt returns the handoff prompt for context compression / new sessions.
func GetCompressionPrompt() string {
	return `Provide a detailed continuation prompt for resuming this work. The new session will NOT have access to our conversation history.

IMPORTANT: Structure your response EXACTLY as follows:

## ORIGINAL GOAL
[State the user's original request/goal in one paragraph]

## COMPLETED ACTIONS (DO NOT REPEAT THESE)
[List specific actions that are DONE and should NOT be repeated. Be specific with file paths, function names, changes made. Use bullet points.]

## CURRENT STATE
[Describe the current state of the codebase/project after the completed actions. What files exist, what has been modified, what is the current status.]

## IN-PROGRESS WORK
[What was being worked on when the context limit was hit? Any partial changes?]

## REMAINING TASKS
[What still needs to be done to complete the original goal? Be specific.]

## NEXT STEP
[What is the immediate next action to take? Be very specific - this is what the agent should do first.]

## KEY CONTEXT
[Any important decisions, constraints, user preferences, technical context or assumptions that must persist.]

Be extremely specific with file paths and function names. The goal is to allow seamless continuation without redoing any completed work.`
}

// GetUpdateCompressionPrompt returns the prompt used when a session that has
// already been compacted needs compacting again. Re-summarizing from scratch
// would discard the earlier summary (the oldest work is no longer in context to
// re-read), so the model is asked to fold the new span into the existing one.
func GetUpdateCompressionPrompt(previousSummary string) string {
	return fmt.Sprintf(`You previously produced this summary of the earlier part of this session:

<previous_summary>
%s
</previous_summary>

Since then, the conversation continued. Produce an UPDATED summary that merges the previous summary with everything that has happened since, using EXACTLY this structure:

## ORIGINAL GOAL
[The user's original request/goal in one paragraph]

## COMPLETED ACTIONS (DO NOT REPEAT THESE)
[Everything already done, from BOTH the previous summary and the new work. Be specific with file paths, function names, and changes made.]

## CURRENT STATE
[Current state of the codebase/project after all completed actions.]

## IN-PROGRESS WORK
[What was being worked on most recently. Any partial changes?]

## REMAINING TASKS
[What still needs to be done to complete the original goal.]

## NEXT STEP
[The immediate next action to take. Be very specific.]

## KEY CONTEXT
[Important decisions, constraints, user preferences, technical context or assumptions that must persist.]

Do not drop information from the previous summary unless it has been superseded. Be extremely specific with file paths and function names.`, previousSummary)
}

// CreateLoopBreakerPrompt returns a system notice when a loop is detected.
func CreateLoopBreakerPrompt(loopDescription string) string {
	return fmt.Sprintf(`
[SYSTEM NOTICE: Loop Detected]

The system has detected that you may be stuck in a repetitive pattern:
%s

To break out of this loop, please:
1. Stop and reflect on what you're trying to accomplish
2. Consider a different approach
3. If the task seems impossible, explain why and ask for clarification
4. If you're encountering repeated errors, try a fundamentally different solution

Do not repeat the same action again.
`, loopDescription)
}
