package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/harsig/harness-kit/gitsource"
	"github.com/harsig/harness-kit/yamlconfig"
	"go.yaml.in/yaml/v3"
)

const (
	providerOpenAI    = "openai_compatible"
	providerAnthropic = "anthropic_compatible"
)

type Config struct {
	MaxIterations     int            `yaml:"max_iterations"`
	Model             ModelConfig    `yaml:"model"`
	SessionDir        string         `yaml:"session_dir"`
	SystemPromptFiles []PromptSource `yaml:"system_prompt_files"`
	MCPServers        []MCPServer    `yaml:"mcp_servers"`
	Setup             Steps          `yaml:"setup"` // commands run in the config directory on every --setup
	dir               string         // directory of the config file, set by applyDefaults
}

// Steps is a list of commands (argv). A single argv (a list of strings) is accepted as one step.
type Steps [][]string

func (s *Steps) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode && len(n.Content) > 0 && n.Content[0].Kind == yaml.ScalarNode {
		var one []string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*s = Steps{one}
		return nil
	}
	var many [][]string
	if err := n.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// resolve resolves each step's path-like program in progDir and its ./ or ../ arguments in configDir.
func (s Steps) resolve(progDir, configDir string) {
	for _, step := range s {
		if len(step) > 0 {
			step[0] = resolveProgram(progDir, step[0])
			resolveArgs(configDir, step[1:])
		}
	}
}

func (s Steps) validate() error {
	for i, step := range s {
		if len(step) == 0 || step[0] == "" {
			return fmt.Errorf("setup[%d] must start with a command", i)
		}
	}
	return nil
}

// resolveProgram makes a path-like program absolute relative to dir; bare names are looked up in PATH.
func resolveProgram(dir, p string) string {
	if strings.Contains(p, "/") {
		return yamlconfig.ResolvePath(dir, p)
	}
	return p
}

// resolveArgs makes arguments starting with ./ or ../ absolute relative to configDir; others are kept as is.
func resolveArgs(configDir string, args []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "./") || strings.HasPrefix(a, "../") {
			args[i] = yamlconfig.ResolvePath(configDir, a)
		}
	}
}

// PromptSource is a local file path (Path) or markdown files from a git repo (Git + Files).
type PromptSource struct {
	Path  string
	Git   *gitsource.Repo
	Files []string
}

func (p *PromptSource) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Path)
	}
	// Node.Decode ignores unknown keys, so decode strictly from re-marshalled YAML.
	raw, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	var m struct {
		Git   *gitsource.Repo `yaml:"git"`
		Files []string        `yaml:"files"`
	}
	if err := yamlconfig.Decode(raw, &m); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	p.Git, p.Files = m.Git, m.Files
	return nil
}

type ModelConfig struct {
	Provider       string `yaml:"provider"`
	Endpoint       string `yaml:"endpoint"`
	Name           string `yaml:"name"`
	APIKey         string `yaml:"api_key"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	MaxTokens      int    `yaml:"max_tokens"`
}

// MCPServer has exactly one of Stdio or HTTP.
type MCPServer struct {
	Name                  string       `yaml:"name"`
	Stdio                 *StdioServer `yaml:"stdio"`
	HTTP                  *HTTPServer  `yaml:"http"`
	ConnectTimeoutSeconds int          `yaml:"connect_timeout_seconds"`
	CallTimeoutSeconds    int          `yaml:"call_timeout_seconds"`
	MaxResultBytes        int          `yaml:"max_result_bytes"`
}

type StdioServer struct {
	Git     *gitsource.Repo   `yaml:"git"`
	Setup   Steps             `yaml:"setup"` // commands run in the working directory on every --setup
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	dir     string            // working directory (the clone, or the config directory without git), set by applyDefaults
}

type HTTPServer struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

var (
	envRef     = regexp.MustCompile(`\$?\$\{([A-Za-z_][A-Za-z0-9_]*)\}`) // a leading $$ escapes the reference
	serverName = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// LoadConfig reads, expands ${VAR} references, strictly decodes, defaults and validates the file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := expandEnv(&doc, false); err != nil {
		return nil, err
	}
	expanded, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yamlconfig.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	baseDir, err := yamlconfig.Dir(path)
	if err != nil {
		return nil, err
	}
	cfg.applyDefaults(baseDir)
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// expandEnv replaces ${VAR} in string scalar values (not map keys); unset variables are errors.
func expandEnv(n *yaml.Node, isKey bool) error {
	// Comments can corrupt the re-marshalled document, so drop them.
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && !isKey {
		var missing string
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			if strings.HasPrefix(m, "$$") {
				return m[1:]
			}
			name := m[2 : len(m)-1]
			v, ok := os.LookupEnv(name)
			if !ok {
				missing = name
			}
			return v
		})
		if missing != "" {
			return fmt.Errorf("environment variable %s is not set", missing)
		}
	}
	for i, c := range n.Content {
		if err := expandEnv(c, n.Kind == yaml.MappingNode && i%2 == 0); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) applyDefaults(baseDir string) {
	setInt := func(v *int, def int) {
		if *v == 0 {
			*v = def
		}
	}
	setInt(&c.MaxIterations, 25)
	setInt(&c.Model.TimeoutSeconds, 600)
	if c.Model.Provider == providerAnthropic {
		setInt(&c.Model.MaxTokens, 8192)
	}
	if c.SessionDir == "" {
		c.SessionDir = "./.plain-harness/sessions"
	}
	c.SessionDir = yamlconfig.ResolvePath(baseDir, c.SessionDir)
	c.dir = baseDir
	c.Setup.resolve(baseDir, baseDir)
	repoCache := filepath.Join(baseDir, ".plain-harness", "repos")
	for i := range c.SystemPromptFiles {
		p := &c.SystemPromptFiles[i]
		if p.Path != "" {
			p.Path = yamlconfig.ResolvePath(baseDir, p.Path)
		}
		if p.Git != nil {
			p.Git.SetDir(repoCache)
		}
	}
	for i := range c.MCPServers {
		s := &c.MCPServers[i]
		setInt(&s.ConnectTimeoutSeconds, 30)
		setInt(&s.CallTimeoutSeconds, 120)
		setInt(&s.MaxResultBytes, 32768)
		if s.Stdio == nil {
			continue
		}
		// The server and its setup run in the clone (or the config directory without git), where path-like
		// programs resolve. Arguments starting with ./ or ../ are relative to the config file instead.
		s.Stdio.dir = baseDir
		if s.Stdio.Git != nil {
			s.Stdio.Git.SetDir(repoCache)
			s.Stdio.dir = s.Stdio.Git.Dir
		}
		s.Stdio.Command = resolveProgram(s.Stdio.dir, s.Stdio.Command)
		resolveArgs(baseDir, s.Stdio.Args)
		s.Stdio.Setup.resolve(s.Stdio.dir, baseDir)
	}
}

func (c *Config) validate() error {
	if c.MaxIterations < 0 {
		return fmt.Errorf("max_iterations must be positive")
	}
	m := c.Model
	if m.TimeoutSeconds < 0 || m.MaxTokens < 0 {
		return fmt.Errorf("model.timeout_seconds and model.max_tokens must be positive")
	}
	switch m.Provider {
	case providerOpenAI:
		if m.MaxTokens != 0 {
			return fmt.Errorf("model.max_tokens is only valid for %s", providerAnthropic)
		}
	case providerAnthropic:
	default:
		return fmt.Errorf("model.provider must be %s or %s", providerOpenAI, providerAnthropic)
	}
	if m.Endpoint == "" || m.Name == "" {
		return fmt.Errorf("model.endpoint and model.name are required")
	}
	for i, p := range c.SystemPromptFiles {
		if err := p.validate(); err != nil {
			return fmt.Errorf("system_prompt_files[%d]: %w", i, err)
		}
	}
	if err := c.Setup.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range c.MCPServers {
		if !serverName.MatchString(s.Name) {
			return fmt.Errorf("mcp_servers: name %q must match %s", s.Name, serverName)
		}
		if seen[s.Name] {
			return fmt.Errorf("mcp_servers: duplicate name %q", s.Name)
		}
		seen[s.Name] = true
		if err := s.validate(); err != nil {
			return fmt.Errorf("mcp_servers[%s]: %w", s.Name, err)
		}
	}
	return nil
}

func (s MCPServer) validate() error {
	if s.ConnectTimeoutSeconds < 0 || s.CallTimeoutSeconds < 0 || s.MaxResultBytes < 0 {
		return fmt.Errorf("connect_timeout_seconds, call_timeout_seconds and max_result_bytes must be positive")
	}
	switch {
	case (s.Stdio == nil) == (s.HTTP == nil):
		return fmt.Errorf("exactly one of stdio or http is required")
	case s.Stdio != nil:
		if s.Stdio.Command == "" {
			return fmt.Errorf("stdio.command is required")
		}
		if err := s.Stdio.Setup.validate(); err != nil {
			return fmt.Errorf("stdio.%w", err)
		}
		if s.Stdio.Git != nil {
			return s.Stdio.Git.Validate()
		}
	case s.HTTP.URL == "":
		return fmt.Errorf("http.url is required")
	}
	return nil
}

func (p PromptSource) validate() error {
	if p.Git == nil {
		if p.Path == "" {
			return fmt.Errorf("must be a file path or a mapping with git and files")
		}
		return nil
	}
	if err := p.Git.Validate(); err != nil {
		return err
	}
	if len(p.Files) == 0 {
		return fmt.Errorf("files is required with git")
	}
	for _, f := range p.Files {
		if !filepath.IsLocal(f) {
			return fmt.Errorf("file %q must be a relative path inside the repo", f)
		}
	}
	return nil
}
