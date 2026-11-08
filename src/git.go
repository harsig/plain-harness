package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/harsig/harness-kit/gitsource"
)

// eachGit visits every git source.
func (c *Config) eachGit(fn func(r *gitsource.Repo) error) error {
	for _, p := range c.SystemPromptFiles {
		if p.Git != nil {
			if err := fn(p.Git); err != nil {
				return err
			}
		}
	}
	for _, s := range c.MCPServers {
		if s.Stdio != nil && s.Stdio.Git != nil {
			if err := fn(s.Stdio.Git); err != nil {
				return err
			}
		}
	}
	return nil
}

// RequireRepos fails, without touching the network, if a git source has not been cloned yet.
func (c *Config) RequireRepos(configPath string) error {
	return c.eachGit(func(r *gitsource.Repo) error {
		return r.Require("plain-harness --config " + configPath + " --setup")
	})
}

// UpdateRepos clones missing repos and fetches and checks out the configured ref of existing ones, then runs
// each stdio server's setup in its working directory and finally the top-level setup in the config directory.
func (c *Config) UpdateRepos(ctx context.Context) error {
	synced := map[string]bool{}
	err := c.eachGit(func(r *gitsource.Repo) error {
		if synced[r.Dir] {
			return nil
		}
		synced[r.Dir] = true
		log.Printf("updating %s", r)
		return r.Sync(ctx)
	})
	if err != nil {
		return err
	}
	for _, s := range c.MCPServers {
		if s.Stdio != nil {
			if err := runSteps(ctx, s.Stdio.dir, s.Stdio.Setup); err != nil {
				return fmt.Errorf("mcp_servers[%s]: setup: %w", s.Name, err)
			}
		}
	}
	if err := runSteps(ctx, c.dir, c.Setup); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	return nil
}

// runSteps runs each command in dir, stopping at the first failure.
func runSteps(ctx context.Context, dir string, steps Steps) error {
	for _, argv := range steps {
		log.Printf("setup in %s: %s", dir, strings.Join(argv, " "))
		if err := runCmd(ctx, dir, argv...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
	}
	return nil
}

// runCmd runs argv in dir with the harness environment; output goes to stderr (stdout carries only answers).
func runCmd(ctx context.Context, dir string, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}
