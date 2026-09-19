package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

// cursorAgent is the reviewer-only direct Cursor CLI path used when the
// trusted repository disables project settings. Cursor's hidden
// --exclude-workspace-context switch is server-gated for the Cursor/Grok
// account used here, so the adapter gives Cursor a disposable clone with all
// project-instruction surfaces removed instead. It never gives this adapter
// the real pipeline worktree for a write-capable turn.
type cursorAgent struct {
	bin                    string
	model                  string
	disableProjectSettings bool
	reviewerOnly           bool
	subprocessContext
}

func (a *cursorAgent) Name() string { return "cursor" }

func (a *cursorAgent) ReportsAgentAttempts() bool { return true }

func (a *cursorAgent) NeutralizesGateInstructions() bool {
	return a.disableProjectSettings && a.reviewerOnly
}

func (a *cursorAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return runWithRetry(ctx, a.Name(), opts, claudeMaxRetries, classifyTransient, nil, func() (*Result, error) {
		return a.runOnce(ctx, opts)
	})
}

func (a *cursorAgent) Close() error { return nil }

func (a *cursorAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	if !a.NeutralizesGateInstructions() {
		return nil, fmt.Errorf("cursor reviewer is not running with verified project-instruction isolation")
	}
	if opts.Purpose != "review" {
		return nil, fmt.Errorf("isolated Cursor is restricted to the read-only review purpose")
	}

	workspace, err := newCursorIsolatedWorkspace(ctx, opts.CWD, a.gitSafeEnv(opts.CWD, opts.Env))
	if err != nil {
		return nil, fmt.Errorf("prepare isolated Cursor workspace: %w", err)
	}
	defer workspace.Close()

	prompt := strings.ReplaceAll(opts.Prompt, opts.CWD, workspace.Path)
	for original, isolated := range workspace.Commits {
		prompt = strings.ReplaceAll(prompt, original, isolated)
	}
	prompt = buildCursorPrompt(prompt, opts.JSONSchema)
	args := a.buildArgs(workspace.Path)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = workspace.Path
	cmd.Stdin = strings.NewReader(prompt)
	cmdEnv := a.gitSafeEnv(workspace.Path, opts.Env)
	cmdEnv = append(cmdEnv,
		"CURSOR_CONFIG_DIR="+workspace.ConfigDir,
		"CURSOR_DATA_DIR="+workspace.DataDir,
		"CURSOR_AGENT_STORE="+workspace.StoreDir,
		"CURSOR_AGENT_STORE_FILES_DIR="+workspace.StoreFilesDir,
	)
	cmd.Env = cmdEnv
	shellenv.ConfigureShellCommand(cmd)

	started, err := startNativeAgentCommand(cmd, nativeAgentActivityObserver(opts, a.Name()))
	if err != nil {
		return nil, fmt.Errorf("cursor start: %w", err)
	}
	defer started.closePipes()
	pid := started.pid()
	emitAgentStarted(opts, a.Name(), pid)

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(started.stderr)
	}()

	stdout, readErr := io.ReadAll(started.stdout)
	if readErr != nil {
		readErr = started.waitAfterParseError(readErr)
		stderrWG.Wait()
		retErr := fmt.Errorf("cursor read output: %w", readErr)
		if detail := cursorStderr(stderrBuf); detail != "" {
			retErr = fmt.Errorf("%w: %s", retErr, detail)
		}
		emitAgentExited(opts, a.Name(), pid, retErr)
		return nil, retErr
	}
	waitErr := started.wait()
	stderrWG.Wait()

	out, parseErr := parseCursorOutput(stdout)
	usage := out.usage()
	if parseErr != nil {
		retErr := fmt.Errorf("cursor output: %w", parseErr)
		if detail := cursorStderr(stderrBuf); detail != "" {
			retErr = fmt.Errorf("%w: %s", retErr, detail)
		}
		emitAgentExited(opts, a.Name(), pid, retErr)
		return resultFromUsage(usage), retErr
	}
	if waitErr != nil {
		retErr := fmt.Errorf("cursor exited: %w", waitErr)
		if detail := cursorErrorDetail(out.Result, stderrBuf); detail != "" {
			retErr = fmt.Errorf("%w: %s", retErr, detail)
		}
		emitAgentExited(opts, a.Name(), pid, retErr)
		return resultFromUsage(usage), retErr
	}
	if out.IsError {
		retErr := fmt.Errorf("cursor reported an error: %s", cursorErrorDetail(out.Result, stderrBuf))
		emitAgentExited(opts, a.Name(), pid, retErr)
		return resultFromUsage(usage), retErr
	}

	out.Result = strings.ReplaceAll(out.Result, workspace.Path, opts.CWD)
	if opts.OnChunk != nil && out.Result != "" {
		opts.OnChunk(out.Result)
	}
	res, err := finalizeTextResult(a.Name(), out.Result, opts.JSONSchema, usage)
	if res != nil {
		res.SessionID = out.SessionID
		res.Provider = a.Name()
	}
	emitAgentExited(opts, a.Name(), pid, err)
	return res, err
}

func (a *cursorAgent) buildArgs(workspace string) []string {
	args := []string{
		"--print",
		"--output-format", "json",
		"--mode", "ask",
		"--trust",
		// This only excludes .cursor/cli.json. The stronger workspace-context
		// flag is intentionally not used: the real Cursor/Grok endpoint rejects
		// it for this account/model, so relying on it would fail open nowhere.
		"--disable-project-configs",
		"--workspace", workspace,
	}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	return args
}

func buildCursorPrompt(prompt string, schema json.RawMessage) string {
	if len(schema) == 0 {
		return prompt
	}
	return prompt + "\n\n## no-mistakes final output contract\n\n" +
		"When the task is complete, your final assistant response must be a single JSON object that matches this JSON Schema. " +
		"Return only the JSON object. Do not wrap it in Markdown fences. Do not include prose before or after the JSON.\n\n" +
		string(schema)
}

type cursorJSONOutput struct {
	Type      string           `json:"type"`
	Subtype   string           `json:"subtype"`
	IsError   bool             `json:"is_error"`
	Result    string           `json:"result"`
	SessionID string           `json:"session_id"`
	Usage     *cursorJSONUsage `json:"usage"`
}

type cursorJSONUsage struct {
	InputTokens         *int `json:"inputTokens"`
	OutputTokens        *int `json:"outputTokens"`
	CacheReadTokens     *int `json:"cacheReadTokens"`
	CacheCreationTokens *int `json:"cacheCreationTokens"`
}

func parseCursorOutput(data []byte) (cursorJSONOutput, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var last cursorJSONOutput
	found := false
	for {
		var candidate cursorJSONOutput
		err := dec.Decode(&candidate)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return cursorJSONOutput{}, fmt.Errorf("decode JSON output: %w", err)
		}
		if candidate.Type == "result" || candidate.Result != "" || candidate.IsError {
			last = candidate
			found = true
		}
	}
	if !found {
		return cursorJSONOutput{}, fmt.Errorf("no result object in output: %q", outputSnippet(string(data)))
	}
	return last, nil
}

func (o cursorJSONOutput) usage() TokenUsage {
	if o.Usage == nil {
		return TokenUsage{}
	}
	usage := TokenUsage{Reported: true}
	if o.Usage.InputTokens != nil {
		usage.InputTokens = *o.Usage.InputTokens
	}
	if o.Usage.OutputTokens != nil {
		usage.OutputTokens = *o.Usage.OutputTokens
	}
	if o.Usage.CacheReadTokens != nil {
		usage.CacheReadTokens = *o.Usage.CacheReadTokens
	}
	if o.Usage.CacheCreationTokens != nil {
		usage.CacheCreationTokens = *o.Usage.CacheCreationTokens
		usage.CacheCreationReported = true
	}
	return usage
}

func cursorStderr(stderr []byte) string {
	return outputSnippet(strings.TrimSpace(string(stderr)))
}

func cursorErrorDetail(result string, stderr []byte) string {
	parts := make([]string, 0, 2)
	if result = strings.TrimSpace(result); result != "" {
		parts = append(parts, outputSnippet(result))
	}
	if detail := cursorStderr(stderr); detail != "" {
		parts = append(parts, detail)
	}
	return strings.Join(parts, "; ")
}

type cursorIsolatedWorkspace struct {
	Root          string
	Path          string
	ConfigDir     string
	DataDir       string
	StoreDir      string
	StoreFilesDir string
	Commits       map[string]string
}

func (w *cursorIsolatedWorkspace) Close() {
	if w != nil && w.Root != "" {
		_ = os.RemoveAll(w.Root)
	}
}

var cursorInstructionFiles = map[string]struct{}{
	"AGENTS.md":         {},
	"CLAUDE.md":         {},
	"CLAUDE.local.md":   {},
	".cursorrules":      {},
	".cursorignore":     {},
	".no-mistakes.yaml": {},
	".grok":             {},
}

var cursorInstructionDirs = map[string]struct{}{
	".cursor": {},
	".agents": {},
	".claude": {},
	".codex":  {},
	".grok":   {},
}

func newCursorIsolatedWorkspace(ctx context.Context, source string, env []string) (*cursorIsolatedWorkspace, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("source worktree is empty")
	}
	status, err := runCursorGit(ctx, source, env, "-C", source, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("check source worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("source worktree is dirty; refusing a stale isolated snapshot")
	}

	root, err := os.MkdirTemp("", "no-mistakes-cursor-")
	if err != nil {
		return nil, fmt.Errorf("create temporary root: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	fail := func(err error) (*cursorIsolatedWorkspace, error) {
		cleanup()
		return nil, err
	}

	workspacePath := filepath.Join(root, "workspace")
	commits, err := exportCursorHistory(ctx, source, workspacePath, root, env)
	if err != nil {
		return fail(err)
	}
	if _, err := runCursorGit(ctx, workspacePath, env, "-C", workspacePath, "config", "--local", "core.hooksPath", os.DevNull); err != nil {
		return fail(fmt.Errorf("disable snapshot hooks: %w", err))
	}
	if err := scrubCursorWorkspace(workspacePath); err != nil {
		return fail(err)
	}
	if err := ensureCursorAncestorsClean(workspacePath); err != nil {
		return fail(err)
	}

	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	storeDir := filepath.Join(root, "store")
	storeFilesDir := filepath.Join(root, "store-files")
	for _, dir := range []string{configDir, dataDir, storeDir, storeFilesDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(fmt.Errorf("create Cursor state directory: %w", err))
		}
	}
	return &cursorIsolatedWorkspace{
		Root:          root,
		Path:          workspacePath,
		ConfigDir:     configDir,
		DataDir:       dataDir,
		StoreDir:      storeDir,
		StoreFilesDir: storeFilesDir,
		Commits:       commits,
	}, nil
}

func runCursorGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, outputSnippet(string(output)))
	}
	return string(output), nil
}

func exportCursorHistory(ctx context.Context, source, workspace, root string, env []string) (map[string]string, error) {
	if _, err := runCursorGit(ctx, root, env, "init", "--template=", workspace); err != nil {
		return nil, err
	}
	originalMarks := filepath.Join(root, "original-marks")
	isolatedMarks := filepath.Join(root, "isolated-marks")
	args := []string{"-C", source, "fast-export", "--export-marks=" + originalMarks, "--full-tree", "--full-history", "--sparse", "HEAD", "--", "."}
	for name := range cursorInstructionFiles {
		args = append(args, ":(glob,exclude)**/"+name)
	}
	for name := range cursorInstructionDirs {
		args = append(args, ":(glob,exclude)**/"+name+"/**")
	}
	export := exec.CommandContext(ctx, "git", args...)
	export.Dir = source
	export.Env = env
	data, err := export.Output()
	if err != nil {
		return nil, fmt.Errorf("export sanitized history: %w", err)
	}
	importCmd := exec.CommandContext(ctx, "git", "-C", workspace, "fast-import", "--export-marks="+isolatedMarks)
	importCmd.Env = env
	importCmd.Stdin = bytes.NewReader(data)
	if output, err := importCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("import sanitized history: %w: %s", err, outputSnippet(string(output)))
	}
	original, err := readCursorMarks(originalMarks)
	if err != nil {
		return nil, err
	}
	isolated, err := readCursorMarks(isolatedMarks)
	if err != nil {
		return nil, err
	}
	commits := make(map[string]string, len(original))
	for mark, sha := range original {
		if isolated[mark] == "" {
			return nil, fmt.Errorf("missing isolated commit for %s", sha)
		}
		commits[sha] = isolated[mark]
	}
	head, err := runCursorGit(ctx, source, env, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if _, err := runCursorGit(ctx, workspace, env, "checkout", "--detach", commits[strings.TrimSpace(head)]); err != nil {
		return nil, err
	}
	return commits, nil
}

func readCursorMarks(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	marks := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid Git export mark %q", line)
		}
		marks[fields[0]] = fields[1]
	}
	return marks, nil
}

func scrubCursorWorkspace(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if path != root && entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("isolated Cursor workspace refuses symlink %q", path)
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if _, ok := cursorInstructionDirs[entry.Name()]; ok {
				if err := os.RemoveAll(path); err != nil {
					return fmt.Errorf("remove project-instruction directory %q: %w", path, err)
				}
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := cursorInstructionFiles[entry.Name()]; ok {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove project-instruction file %q: %w", path, err)
			}
		}
		return nil
	})
}

func ensureCursorAncestorsClean(workspace string) error {
	for dir := filepath.Dir(workspace); ; dir = filepath.Dir(dir) {
		for name := range cursorInstructionFiles {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				return fmt.Errorf("Cursor workspace ancestor contains project-instruction file %q", filepath.Join(dir, name))
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect Cursor workspace ancestor %q: %w", dir, err)
			}
		}
		for name := range cursorInstructionDirs {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				return fmt.Errorf("Cursor workspace ancestor contains project-instruction directory %q", filepath.Join(dir, name))
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect Cursor workspace ancestor %q: %w", dir, err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
	}
}
