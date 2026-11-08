# plain-harness

`plain-harness` runs an LLM agent configured in YAML. It supports OpenAI-compatible and Anthropic-compatible model APIs, MCP tools, optional system prompts, and persistent conversation sessions.

## Build and configure

Requires Go (see [src/go.mod](src/go.mod) for the version). Shared code comes from [harness-kit](https://github.com/harsig/harness-kit). Until it has tagged releases, it is tracked at its `main` branch; update it with `go get github.com/harsig/harness-kit@main` in `src/`.

From this directory, build the CLI:

```sh
./build.sh
```

Create a `plain-harness.yml` for your model and tools. The full schema is in [spec.md](./spec.md#configuration-schema); a minimal configuration is:

```yaml
model:
  provider: openai_compatible
  endpoint: http://localhost:8080/v1
  name: "model-name"
  api_key: "${LLM_API_KEY}"
```

Configuration values use `${VAR}` for environment-variable expansion; an unset variable is an error. Relative paths are resolved from the configuration file's directory.

If the configuration uses git-based system prompts or stdio MCP servers, initialize or update those sources before running:

```sh
./bin/plain-harness --setup
```

This clones missing repositories, fetches and checks out configured refs for existing repositories, and runs the configured setup commands: each stdio server's `setup` in its working directory (the clone, or the config directory without git), then the top-level `setup` in the config directory. In `command`, `args` and `setup`, arguments starting with `./` or `../` are relative to the config file; a path-like program resolves in the working directory. Setup requires `git`; later agent runs use the local clones and do not fetch.

## Run

```sh
./bin/plain-harness [options] "<message>"
```

The message must be one quoted argument. Options must come before it:

```sh
./bin/plain-harness "Summarize the files in the current workspace"
./bin/plain-harness --session work "Find the main entry point"
./bin/plain-harness --session work --stream "Explain how the application starts"
./bin/plain-harness --config ./configs/review.yml "Review the changes"
```

Options:

| Option | Description |
| --- | --- |
| `--config <file>` | YAML configuration path. Defaults to `plain-harness.yml` in the current directory. |
| `--session <name>` | Continue and save conversation history under this name. Omit for a stateless run. |
| `--stream` | Print model text as it is generated. Off by default. |
| `--version` | Print the version and exit. |
| `--setup` | Prepare configured git sources and run their setup commands instead of running the agent. Cannot be combined with a message, `--session`, or `--stream`. |

Answers are written to stdout; logs and errors go to stderr. Session files are stored in the configured `session_dir` (default: `.plain-harness/sessions`, relative to the config file).