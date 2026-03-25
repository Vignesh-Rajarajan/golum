package prompt

// PromptConfig carries context for building the system prompt (working directory,
// optional project/user instructions). API keys and model settings stay in
// pkg/config; this struct is only for prompt text.
type PromptConfig struct {
	CWD                   string
	DeveloperInstructions string
	UserInstructions      string
}
