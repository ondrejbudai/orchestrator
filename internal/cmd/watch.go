package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/drellabot/orchestrator/internal/daemon"
	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/spf13/cobra"
)

var watchTimeout string

var taskWatchCmd = &cobra.Command{
	Use:   "watch <task-name> <pr-url>",
	Short: "Poll a single PR for new comments (debug tool)",
	Long: `Polls a single PR for new comments from allowed commenters, prints the
formatted prompt that would be sent to 'task continue', and exits.
Does not modify state.json.

Blocks until a new comment is found or --timeout is reached. Without
--timeout, polls indefinitely until interrupted (Ctrl-C).`,
	Args: cobra.ExactArgs(2),
	RunE: runTaskWatch,
}

func init() {
	taskWatchCmd.Flags().StringVar(&watchTimeout, "timeout", "", "stop waiting after this duration (e.g. 30s, 5m)")
}

func runTaskWatch(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	prURL := args[1]

	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if watchTimeout != "" {
		d, err := time.ParseDuration(watchTimeout)
		if err != nil {
			return fmt.Errorf("parsing --timeout: %w", err)
		}
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, d)
		defer timeoutCancel()
	}

	ghRunner := gh.New("")

	prompt, err := daemon.WatchPR(ctx, ghRunner, cfg.OutputDir, taskName, prURL, cfg.Daemon.AllowedCommenters, 5*time.Second)
	if err != nil {
		return err
	}

	fmt.Print(prompt)
	return nil
}
