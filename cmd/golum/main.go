package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui"
	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/joho/godotenv"
)

func main() {
	defer applog.RecoverMain()
	// Load .env before applog.Init so GOLUM_LOG in .env is visible (Init used to run before godotenv).
	_ = godotenv.Load()
	applog.Init()

	var (
		listSessions = flag.Bool("list", false, "list saved sessions and exit")
		resumeID     = flag.String("resume", "", "resume a saved session by id (see -list)")
		continueLast = flag.Bool("continue", false, "resume the most recently updated session")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{
			OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
			OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
			BaseURL:          os.Getenv("OPENAI_BASE_URL"),
			Model:            getEnvWithDefault("OPENAI_MODEL", "gpt-4o"),
		}
	}

	// -list needs the database but not an API key.
	if *listSessions {
		if err := printSessions(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}

	if cfg.OpenAIAPIKey == "" && cfg.OpenRouterAPIKey == "" {
		fmt.Println("Error: No API key found. Set OPENAI_API_KEY or OPENROUTER_API_KEY environment variable.")
		os.Exit(1)
	}

	resume := *resumeID
	if *continueLast && resume == "" {
		id, err := mostRecentSessionID(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		if id == "" {
			fmt.Println("No saved sessions to continue.")
			os.Exit(1)
		}
		resume = id
	}

	m := ui.NewModel(cfg, ui.Options{ResumeSessionID: resume})
	p := tea.NewProgram(m)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func openStore(cfg *config.Config) (*session.SQLiteStore, error) {
	path, err := session.DefaultDBPath()
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	return session.OpenSQLiteStore(path, cfg, prompt.PromptConfig{CWD: cwd}, nil)
}

func printSessions(cfg *config.Config) error {
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	// Pick up anything not yet migrated so -list shows a complete picture.
	if dir, err := session.DefaultSessionsDir(); err == nil {
		_, _ = session.MigrateJSONL(context.Background(), store, dir)
	}

	list, err := store.List(context.Background())
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No saved sessions.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUPDATED\tENTRIES\tDIR\tLABEL")
	for _, m := range list {
		dir := ""
		if m.CWD != "" {
			dir = filepath.Base(m.CWD)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			m.ID, humanTime(m.UpdatedAt), m.EntryCount, dir, m.Label)
	}
	return w.Flush()
}

func mostRecentSessionID(cfg *config.Config) (string, error) {
	store, err := openStore(cfg)
	if err != nil {
		return "", err
	}
	defer store.Close()

	if dir, err := session.DefaultSessionsDir(); err == nil {
		_, _ = session.MigrateJSONL(context.Background(), store, dir)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) == 0 {
		return "", err
	}
	return list[0].ID, nil
}

func humanTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
