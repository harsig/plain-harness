# plain-harness

**plain-harness** is a small Go CLI for Linux and other Unix systems that runs an LLM agent defined by one YAML file. It builds an optional system prompt from markdown files, runs a tool-calling loop, and exposes tools from MCP servers. Sessions persist between runs when you pass `--session <session_name>`.

## Features

- **One agent per YAML configuration file**, with one model. Another model or provider means another file.
- **Two LLM API types are supported:** OpenAI-compatible (llama.cpp, OpenAI, custom) and Anthropic-compatible.
- **System prompt** from a list of markdown files from the configuration file, concatenated in order. Files can come from a git repository.
- **MCP tools** from stdio processes (started by the harness, optionally from a git repository) or HTTP servers (started by you).
- **Setup commands:** after updating the git sources, `--setup` runs each stdio server's `setup` commands in that server's working directory, then the top-level `setup` commands in the config file's directory, in order; the first failure stops setup with an error. A top-level step can run `plain-harness --setup` for another configuration (e.g. a sub-agent), so one call sets up a whole chain.
- **Command paths:** for `command`, `args` and `setup`, a path-like program (containing `/`) resolves in the working directory (the clone with `git`, otherwise the config file's directory); a bare name is looked up in `PATH`. Arguments that start with `./` or `../` are relative to the config file and are passed as absolute paths; all other arguments are passed unchanged.
- **Git sources:** `plain-harness --setup` clones each repository into `.plain-harness/repos/<name>-<hash>/` next to the configuration file (the hash covers URL and `ref`), or fetches it if it is already there, and checks out the `ref` (tag, branch or commit; default: the remote's default branch). A normal run uses these clones and never touches the network; it fails with a hint to run `--setup` if a clone is missing, which includes after a `ref` change. Requires the `git` binary; authentication is whatever your git setup provides (SSH for private repositories).
- **File sessions:** Session stored in one JSON file per session, atomic saves, and an exclusive lock so two runs can't use the same session. Without `--session <session_name>`, a run is stateless.
- **Agent loop:** capped at `max_iterations`. Tool errors go back to the model as error results so it can recover. Network errors, timeouts, HTTP 429 and 5xx are retried, 3 attempts in total (honoring `Retry-After`, capped at 30 seconds).
- **Prompt caching:** Anthropic-compatible requests enable automatic prompt caching (top-level `cache_control`), so each turn reads the earlier turns from the cache. OpenAI-compatible servers cache prompt prefixes on their own.
- **Config checks at startup:** unknown keys fail, `${VAR}` is expanded in string values on the configuration file and an unset variable is an error; write `$${VAR}` for a literal `${VAR}`. Relative paths are resolved from the config file's directory.
- **Streaming:** `--stream` writes the model's text to stdout as it is generated, for every turn. Off by default. Streaming runs use an idle timeout instead of a whole-request timeout. A retry is only allowed before any text has been printed.
- **CLI:** `plain-harness [--config <config_file>] [--session <session_name>] [--stream] <message>` runs the agent; `plain-harness [--config <config_file>] --setup` clones missing git sources, fetches and checks out the configured `ref` of existing ones, and runs the `setup` commands; `plain-harness --version` prints the version, which `build.sh` takes from `git describe`. `--config` defaults to `./plain-harness.yml`. The message is a single (quoted) argument after the options; no message (or `--setup` combined with a message) is a usage error. Answer on stdout.
- **Errors:** usage errors print the usage and exit with code 2; configuration, setup and startup problems exit with code 1 and a clear message. A failing tool call goes back to the model; a failing model turn ends the run with code 1, and the session is still saved in a valid state. Logs and errors go to stderr, prefixed with `plain-harness: `.
- **Platform:** Linux and other Unix systems.
- **Code layout:** a single `main` package: `main.go` (wiring, CLI), `config.go`, `git.go`, `session.go`, `agent.go` (loop and retries), `mcp.go` (MCP client and tool routing), `http.go`, `openai.go`, `anthropic.go` (provider adapters), with table-driven tests per file. Git sources, strict YAML loading, path resolution and output truncation come from the shared module `github.com/harsig/harness-kit`. No interfaces beyond `Provider`.
- **Out of scope:** more than one model per configuration, context compaction (sessions grow until the model's limit), MCP resources, prompts and sampling, non-text tool results, parallel tool execution, a TUI or interactive mode.

## Configuration Schema

```yaml
max_iterations: 25              # optional, default 25

model:
  provider: openai_compatible   # openai_compatible | anthropic_compatible
  endpoint: http://localhost:8080/v1   # openai: incl. /v1; anthropic: no /v1
  name: "model-name"
  api_key: ""                   # optional
  timeout_seconds: 600          # optional, per request (idle limit when streaming)
  max_tokens: 8192              # anthropic_compatible only, default 8192

session_dir: ./.plain-harness/sessions  # optional, default ./.plain-harness/sessions

setup:                          # optional commands run in the config directory on every `--setup`, after the servers' setup
  - ["../../plain-harness/bin/plain-harness", "--setup", "--config", "../sub-agent/plain-harness.yml"]

system_prompt_files:            # optional, concatenated in order
  - ./prompts/rules.md          # local file
  - git:                        # or files from a git repository
      url: https://github.com/acme/prompts.git
      ref: v1.4.0               # optional: tag | branch | commit (default: remote default branch)
    files: [base/persona.md]    # required, relative to the repository root

mcp_servers:                    # optional; each server has exactly one of stdio | http
  - name: "fs"                  # ^[a-z0-9-]+$, unique
    connect_timeout_seconds: 30 # optional
    call_timeout_seconds: 120   # optional
    max_result_bytes: 32768     # optional
    stdio:
      git:                      # optional: run from a git repository
        url: git@github.com:acme/mcp-fs.git
        ref: v2.1.0             # optional
      setup:                    # optional commands run in the working directory on every `--setup`
        - ["npm", "ci"]         # (a single command may be written as one list: setup: ["npm", "ci"])
        - ["node", "dist/server.js", "--setup", "./fs.yml"]
      command: node             # path-like programs resolve in the working directory
      args: ["dist/server.js", "./fs.yml"]  # working directory: the clone, or the config directory without git;
                                # ./ and ../ arguments are relative to the config file
      env: {}                   # added to HOME, LOGNAME, PATH, SHELL, TERM, USER (the only inherited vars)

  - name: "remote"
    http:
      url: https://mcp.example.com/mcp
      headers: {}
```
