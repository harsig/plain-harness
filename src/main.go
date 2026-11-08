package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const defaultConfig = "plain-harness.yml"

var version = "dev" // set by build.sh

func main() {
	log.SetFlags(0)
	log.SetPrefix("plain-harness: ")
	fs := flag.NewFlagSet("plain-harness", flag.ExitOnError)
	configPath := fs.String("config", defaultConfig, "path to the agent YAML configuration (default "+defaultConfig+")")
	sessionID := fs.String("session", "", "session name; omit for a stateless run")
	stream := fs.Bool("stream", false, "print model text as it is generated")
	doSetup := fs.Bool("setup", false, "clone missing git sources, fetch and check out the configured ref of existing ones, and run setup commands (instead of running the agent)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage:\n"+
			"  plain-harness [options] <message>\n"+
			"  plain-harness [--config <file>] --setup\n\n"+
			"Options:\n")
		fs.VisitAll(func(f *flag.Flag) {
			fmt.Fprintf(fs.Output(), "  --%s\n    \t%s\n", f.Name, f.Usage)
		})
	}
	fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("plain-harness", version)
		return
	}

	usageErr := func(msg string) {
		fmt.Fprintf(fs.Output(), "error: %s\n\n", msg)
		fs.Usage()
		os.Exit(2)
	}
	message := fs.Arg(0)
	switch {
	case *doSetup && (fs.NArg() > 0 || *sessionID != "" || *stream):
		usageErr("--setup cannot be combined with a message, --session or --stream")
	case !*doSetup && fs.NArg() == 0:
		usageErr("no message given")
	case fs.NArg() > 1:
		usageErr("expected a single message argument; quote it and put options before it")
	case !*doSetup && strings.TrimSpace(message) == "":
		usageErr("message is empty")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if *doSetup {
		err = runSetup(ctx, *configPath)
	} else {
		err = run(ctx, *configPath, *sessionID, message, *stream)
	}
	if err != nil {
		stop()
		log.Fatal(err)
	}
}

func runSetup(ctx context.Context, configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	return cfg.UpdateRepos(ctx)
}

func run(ctx context.Context, configPath, sessionID, message string, stream bool) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := cfg.RequireRepos(configPath); err != nil {
		return err
	}
	system, err := loadSystemPrompt(cfg.SystemPromptFiles)
	if err != nil {
		return err
	}

	var session *Session
	if sessionID != "" {
		if session, err = OpenSession(cfg.SessionDir, sessionID); err != nil {
			return err
		}
		defer session.Close()
	}

	tools, err := ConnectTools(ctx, cfg.MCPServers)
	if err != nil {
		return err
	}
	defer tools.Close()

	var provider Provider = newOpenAI(cfg.Model)
	if cfg.Model.Provider == providerAnthropic {
		provider = newAnthropic(cfg.Model)
	}
	agent := &Agent{provider: provider, tools: tools, system: system, maxIterations: cfg.MaxIterations}

	var onText func(string)
	atLineStart := true // whether streamed output so far ends with a newline
	if stream {
		onText = func(s string) {
			fmt.Print(s)
			atLineStart = strings.HasSuffix(s, "\n")
		}
	}
	var history []Message
	if session != nil {
		history = session.Messages
	}
	history, answer, runErr := agent.Run(ctx, history, message, onText)

	if session != nil {
		session.Messages = history
		if err := session.Save(); err != nil {
			return fmt.Errorf("save session: %w", err)
		}
	}
	if !atLineStart {
		fmt.Println() // end a partial output line, e.g. after an error mid-stream
	}
	if runErr != nil {
		return runErr
	}
	if !stream {
		fmt.Println(answer)
	}
	return nil
}

func loadSystemPrompt(sources []PromptSource) (string, error) {
	var parts []string
	for _, s := range sources {
		files := []string{s.Path}
		if s.Git != nil {
			files = nil
			for _, f := range s.Files {
				files = append(files, filepath.Join(s.Git.Dir, f))
			}
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return "", fmt.Errorf("system prompt: %w", err)
			}
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n\n"), nil
}
