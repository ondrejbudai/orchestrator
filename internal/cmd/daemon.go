package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	gh "github.com/drellabot/orchestrator/internal/github"

	"github.com/drellabot/orchestrator/internal/daemon"
	"github.com/spf13/cobra"
)

var daemonInterval string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Poll GitHub PRs for new comments and trigger task continue",
	Long: `The daemon polls all open PRs tracked in task state files, checks for new
comments from allowed users, and automatically runs 'task continue' with
those comments as the prompt.`,
	RunE: runDaemon,
}

func init() {
	daemonCmd.Flags().StringVar(&daemonInterval, "interval", "", "poll interval (e.g. 60s, 5m); overrides config")
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	interval := 60 * time.Second
	// Config takes precedence over default
	if cfg.Daemon.PollInterval != "" {
		parsed, err := time.ParseDuration(cfg.Daemon.PollInterval)
		if err != nil {
			return fmt.Errorf("parsing daemon.poll_interval: %w", err)
		}
		interval = parsed
	}
	// Flag takes precedence over config
	if daemonInterval != "" {
		parsed, err := time.ParseDuration(daemonInterval)
		if err != nil {
			return fmt.Errorf("parsing --interval: %w", err)
		}
		interval = parsed
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ghRunner := gh.New("")
	if _, err := ghRunner.AuthenticatedUser(ctx); err != nil {
		return fmt.Errorf("GitHub CLI not authenticated: %w", err)
	}

	if len(cfg.Daemon.AllowedCommenters) == 0 {
		slog.Warn("daemon.allowed_commenters is empty; no comments will trigger task continue")
	}

	d := daemon.New(ghRunner, interval, configPath, cfg.OutputDir, cfg.Daemon.AllowedCommenters)

	slog.Info("Daemon starting", "interval", interval, "output_dir", cfg.OutputDir, "allowed_commenters", cfg.Daemon.AllowedCommenters)
	return d.Run(ctx)
}
