package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeConfig(t *testing.T, dir, repo, ref string) string {
	t.Helper()
	cfg := `
model: {provider: openai_compatible, endpoint: http://x/v1, name: m}
system_prompt_files:
  - git: {url: ` + repo + `, ref: ` + ref + `}
    files: [a.md]
mcp_servers:
  - name: tool
    stdio:
      git: {url: ` + repo + `, ref: ` + ref + `}
      setup: [sh, -c, "echo built > built.txt"]
      command: ./run.sh
`
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpdateAndRun(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.md"), []byte("v1"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "1")
	git(t, repo, "tag", "v1")
	os.WriteFile(filepath.Join(repo, "a.md"), []byte("v2"), 0o644)
	git(t, repo, "commit", "-qam", "2")
	git(t, repo, "tag", "v2")

	conf := filepath.Join(root, "conf")
	os.Mkdir(conf, 0o755)
	path := writeConfig(t, conf, repo, "v1")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireRepos(path); err == nil || !strings.Contains(err.Error(), "plain-harness --config") {
		t.Fatalf("want update hint, got %v", err)
	}
	for i := 0; i < 2; i++ { // second pass: existing clone is fetched, not recloned
		if err := cfg.UpdateRepos(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.RequireRepos(path); err != nil {
		t.Fatal(err)
	}
	got, _ := loadSystemPrompt(cfg.SystemPromptFiles)
	if got != "v1" {
		t.Fatalf("prompt = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.SystemPromptFiles[0].Git.Dir, "built.txt")); err != nil {
		t.Fatalf("setup did not run: %v", err)
	}

	// A changed ref is a different clone: stale until update.
	path = writeConfig(t, conf, repo, "v2")
	if cfg, err = LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireRepos(path); err == nil {
		t.Fatal("changed ref should require update")
	}
	if err := cfg.UpdateRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSystemPrompt(cfg.SystemPromptFiles); got != "v2" {
		t.Fatalf("prompt = %q, want v2", got)
	}

	// A branch ref follows new upstream commits on update.
	path = writeConfig(t, conf, repo, "main")
	cfg, _ = LoadConfig(path)
	cfg.UpdateRepos(context.Background())
	os.WriteFile(filepath.Join(repo, "a.md"), []byte("v3"), 0o644)
	git(t, repo, "commit", "-qam", "3")
	if err := cfg.UpdateRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSystemPrompt(cfg.SystemPromptFiles); got != "v3" {
		t.Fatalf("prompt = %q, want v3", got)
	}

	// Unknown ref must not leave a clone behind.
	path = writeConfig(t, conf, repo, "nope")
	cfg, _ = LoadConfig(path)
	if err := cfg.UpdateRepos(context.Background()); err == nil {
		t.Fatal("unknown ref should fail")
	}
	if cfg.RequireRepos(path) == nil {
		t.Fatal("failed clone must not count as cloned")
	}
}

func TestConfigValidation(t *testing.T) {
	base := "model: {provider: openai_compatible, endpoint: http://x/v1, name: m}\n"
	cases := map[string]string{
		"empty setup step":      "mcp_servers:\n  - name: a\n    stdio: {command: c, setup: [[x], []]}\n",
		"setup without program": "mcp_servers:\n  - name: a\n    stdio: {command: c, setup: [\"\"]}\n",
		"empty top-level step":  "setup: [[]]\n",
		"ref outside git":       "mcp_servers:\n  - name: a\n    stdio: {command: c, ref: v1}\n",
		"git without url":       "mcp_servers:\n  - name: a\n    stdio: {command: c, git: {ref: v1}}\n",
		"both transports":       "mcp_servers:\n  - name: a\n    stdio: {command: c}\n    http: {url: u}\n",
		"git on http":           "mcp_servers:\n  - name: a\n    http: {url: u, git: {url: g}}\n",
		"prompt without git":    "system_prompt_files:\n  - files: [a.md]\n",
		"prompt without files":  "system_prompt_files:\n  - git: {url: g}\n",
		"prompt escapes repo":   "system_prompt_files:\n  - git: {url: g}\n    files: [../x.md]\n",
		"prompt unknown key":    "system_prompt_files:\n  - git: {url: g}\n    files: [a.md]\n    ref: v1\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "c.yaml")
		os.WriteFile(p, []byte(base+body), 0o644)
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSetupSteps(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "build.sh"), []byte("#!/bin/sh\necho built > built.txt\n"), 0o755)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "1")

	conf := filepath.Join(root, "conf")
	os.Mkdir(conf, 0o755)
	// Each step records its working directory and arguments; ./ arguments resolve to the config directory.
	cfg := `
model: {provider: openai_compatible, endpoint: http://x/v1, name: m}
setup: [sh, -c, 'pwd > "$0"', ./top.txt]
mcp_servers:
  - name: cloned
    stdio:
      git: {url: ` + repo + `}
      setup:
        - [./build.sh]
        - [sh, -c, 'pwd > "$0"', ./cloned.txt]
      command: ./run.sh
  - name: local
    stdio:
      setup: [[sh, -c, 'pwd > "$0"', ./local.txt]]
      command: ./run.sh
`
	path := filepath.Join(conf, "config.yaml")
	os.WriteFile(path, []byte(cfg), 0o644)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	clone := c.MCPServers[0].Stdio.Git.Dir
	if _, err := os.Stat(filepath.Join(clone, "built.txt")); err != nil {
		t.Errorf("./build.sh did not run in the clone: %v", err)
	}
	for file, wantDir := range map[string]string{"cloned.txt": clone, "local.txt": conf, "top.txt": conf} {
		got, err := os.ReadFile(filepath.Join(conf, file))
		if err != nil {
			t.Errorf("%s not written to the config directory: %v", file, err)
			continue
		}
		if strings.TrimSpace(string(got)) != wantDir {
			t.Errorf("%s: ran in %q, want %q", file, strings.TrimSpace(string(got)), wantDir)
		}
	}

	// A failing step stops setup with an error naming the server.
	os.WriteFile(path, []byte(strings.Replace(cfg, "[./build.sh]", "[\"false\"]", 1)), 0o644)
	c, _ = LoadConfig(path)
	if err := c.UpdateRepos(context.Background()); err == nil || !strings.Contains(err.Error(), "mcp_servers[cloned]") {
		t.Fatalf("want setup error for cloned, got %v", err)
	}
}
