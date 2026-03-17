package cmd

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellabot/orchestrator/internal/config"
	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/drellabot/orchestrator/internal/gjoll"
	"github.com/drellabot/orchestrator/internal/logging"
	mcpserver "github.com/drellabot/orchestrator/internal/mcp"
	"github.com/drellabot/orchestrator/internal/task"
	"github.com/spf13/cobra"
)

//go:embed prompt.md
var promptContent string

var author string

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage tasks",
}

var taskNewCmd = &cobra.Command{
	Use:   "new <task-name> <task-description...>",
	Short: "Run a new task in a sandboxed Claude instance",
	Long: `Provisions a sandbox VM via gjoll, starts an MCP server for code pulling,
launches Claude with the task description, and archives the results.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runTask,
}

var taskContinueCmd = &cobra.Command{
	Use:   "continue <task-name> <task-description...>",
	Short: "Continue a stopped task with a new prompt",
	Long: `Resumes a stopped sandbox VM, starts an MCP server, and launches Claude
with --continue to resume the previous conversation with a new prompt.`,
	Args: cobra.MinimumNArgs(2),
	RunE: continueTask,
}

func init() {
	taskNewCmd.Flags().StringVar(&author, "author", "", "co-author to add to PR commits (e.g. \"Jane Doe <jane@example.com>\")")
	taskContinueCmd.Flags().StringVar(&author, "author", "", "co-author to add to PR commits (e.g. \"Jane Doe <jane@example.com>\")")
	taskCmd.AddCommand(taskNewCmd)
	taskCmd.AddCommand(taskContinueCmd)
	taskCmd.AddCommand(taskWatchCmd)
}

func runTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	taskDescription := strings.Join(args[1:], " ")

	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ghRunner := logPreflightWarnings(ctx, cfg)

	slog.Info("Task started", "task", taskName)

	taskDir, err := task.Create(cfg.OutputDir, taskName)
	if err != nil {
		return fmt.Errorf("creating task directory: %w", err)
	}

	if err := taskDir.SaveMetadata(taskName, taskDescription, time.Now()); err != nil {
		return fmt.Errorf("saving metadata: %w", err)
	}

	return executeTask(ctx, taskName, taskDescription, taskDir, cfg, ghRunner, false, author)
}

func continueTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	taskDescription := strings.Join(args[1:], " ")

	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ghRunner := logPreflightWarnings(ctx, cfg)

	slog.Info("Task continuing", "task", taskName)

	taskDir, err := task.Open(cfg.OutputDir, taskName)
	if err != nil {
		return fmt.Errorf("opening task directory: %w", err)
	}

	return executeTask(ctx, taskName, taskDescription, taskDir, cfg, ghRunner, true, author)
}

func loadConfigAndSetupLogging() (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	logger := logging.SetupLogger(cfg.SlackWebhook, verbose)
	slog.SetDefault(logger)

	return cfg, nil
}

func logPreflightWarnings(ctx context.Context, cfg *config.Config) *gh.Runner {
	if len(cfg.AllowedRepos) == 0 {
		slog.Warn("allowed_repos is empty; open_pr, update_pr, and comment_on_pr tools will not be available")
	}

	ghRunner := gh.New("")
	if _, err := ghRunner.AuthenticatedUser(ctx); err != nil {
		slog.Warn("GitHub CLI not authenticated; open_pr, update_pr, and comment_on_pr tools will not be available", "error", err)
	}

	return ghRunner
}

func executeTask(ctx context.Context, taskName, taskDescription string, taskDir *task.Dir, cfg *config.Config, ghRunner *gh.Runner, continueSession bool, author string) error {
	runner := gjoll.New("")

	logger := slog.Default()

	// Start MCP server
	mcpSrv := mcpserver.New(logger, taskName, taskDir, runner, ghRunner, cfg.AllowedRepos, author)
	if err := mcpSrv.Start(); err != nil {
		return fmt.Errorf("starting MCP server: %w", err)
	}
	defer func() { _ = mcpSrv.Stop(context.Background()) }()

	mcpPort := mcpSrv.Port()
	mcpTunnel := fmt.Sprintf("%d:localhost:%d", mcpserver.MCPRemotePort, mcpPort)
	slog.Debug("MCP server started", "task", taskName, "port", mcpPort)

	if continueSession {
		slog.Info("Resuming sandbox", "task", taskName)
		if err := runner.Start(ctx, taskName); err != nil {
			return fmt.Errorf("resuming sandbox: %w", err)
		}
	} else {
		tfPath, err := filepath.Abs(cfg.GjollEnv)
		if err != nil {
			return fmt.Errorf("resolving tf path: %w", err)
		}

		slog.Info("Provisioning sandbox", "task", taskName)
		if err := runner.Up(ctx, taskName, tfPath); err != nil {
			return fmt.Errorf("provisioning sandbox: %w", err)
		}
	}

	// After successful provisioning, ensure cleanup on exit
	defer func() {
		slog.Debug("Copying transcript", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, ":~/transcript.jsonl", taskDir.TranscriptPath()); copyErr != nil {
			slog.Warn("Failed to copy transcript", "task", taskName, "error", copyErr)
		}

		slog.Debug("Copying conversations", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, ":~/.claude/", taskDir.ConversationsPath()); copyErr != nil {
			slog.Warn("Failed to copy conversations", "task", taskName, "error", copyErr)
		}

		// Flush filesystem writes before stopping — the libvirt provider
		// only waits 5 seconds for graceful ACPI shutdown before force-
		// destroying the VM, which can lose unflushed data.
		slog.Debug("Syncing filesystem", "task", taskName)
		_ = runner.SSH(context.Background(), taskName, "sync")

		slog.Debug("Stopping sandbox", "task", taskName)
		if stopErr := runner.Stop(context.Background(), taskName); stopErr != nil {
			slog.Warn("Failed to stop sandbox", "task", taskName, "error", stopErr)
		}
	}()

	slog.Info("Sandbox provisioned", "task", taskName)

	if !continueSession {
		if err := setupSandbox(ctx, runner, taskName); err != nil {
			return fmt.Errorf("setting up sandbox: %w", err)
		}
		slog.Debug("Sandbox setup complete", "task", taskName)
	}

	// Build the Claude run script
	slog.Info("Running Claude", "task", taskName)
	runScript := buildRunScript(taskDescription, continueSession)

	tmpRun, err := os.CreateTemp("", "run-claude-*.sh")
	if err != nil {
		return fmt.Errorf("creating run script: %w", err)
	}
	defer os.Remove(tmpRun.Name())
	if _, err := tmpRun.WriteString(runScript); err != nil {
		tmpRun.Close()
		return fmt.Errorf("writing run script: %w", err)
	}
	tmpRun.Close()

	if err := runner.Cp(ctx, taskName, tmpRun.Name(), ":/tmp/run-claude.sh"); err != nil {
		return fmt.Errorf("copying run script: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "chmod +x /tmp/run-claude.sh"); err != nil {
		return fmt.Errorf("making run script executable: %w", err)
	}

	sshOpts := &gjoll.SSHOpts{
		Proxy:          true,
		ReverseTunnels: []string{mcpTunnel},
	}

	tw := newTranscriptWriter(os.Stdout, verbose)
	if err := runner.SSHProxyOutput(ctx, taskName, tw, sshOpts, "/tmp/run-claude.sh"); err != nil {
		slog.Error("Claude exited with error", "task", taskName, "error", err)
		// Don't return error - still want to archive results
	}

	slog.Info("Claude finished", "task", taskName)
	slog.Info("Task completed", "task", taskName)
	return nil
}

func buildRunScript(taskDescription string, continueSession bool) string {
	escapedDesc := strings.ReplaceAll(taskDescription, "'", "'\\''")

	var claudeFlags string
	var teeFlag string
	if continueSession {
		claudeFlags = "--continue"
		teeFlag = "-a"
	}

	return fmt.Sprintf(`#!/bin/bash
source ~/.bashrc
stdbuf -oL claude --dangerously-skip-permissions -p --verbose \
  --output-format stream-json --append-system-prompt-file ~/system-prompt.md \
  %s '%s' \
  </dev/null | stdbuf -oL tee %s ~/transcript.jsonl
`, claudeFlags, escapedDesc, teeFlag)
}

func setupSandbox(ctx context.Context, runner *gjoll.Runner, taskName string) error {
	// Configure git
	if err := runner.SSH(ctx, taskName, "git config --global user.name Drellabot"); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "git config --global user.email imagebuilder-bots+drella@redhat.com"); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}

	// Write system prompt to a temp file and copy it to the sandbox
	tmpFile, err := os.CreateTemp("", "prompt-*.md")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(promptContent); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing prompt: %w", err)
	}
	tmpFile.Close()

	if err := runner.Cp(ctx, taskName, tmpFile.Name(), ":~/system-prompt.md"); err != nil {
		return fmt.Errorf("copying system prompt: %w", err)
	}

	// Register MCP server with Claude
	mcpURL := fmt.Sprintf("http://localhost:%d/mcp", mcpserver.MCPRemotePort)
	if err := runner.SSH(ctx, taskName, fmt.Sprintf("claude mcp add --transport http orchestrator %s --scope user", mcpURL)); err != nil {
		return fmt.Errorf("registering MCP server: %w", err)
	}

	return nil
}
