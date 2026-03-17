package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	configPath string
	verbose    bool
)

var rootCmd = &cobra.Command{
	Use:   "orchestrator",
	Short: "Orchestrate agentic Claude sandboxes",
	Long: `Orchestrator spawns sandboxed Claude instances using gjoll (libvirt backend),
exposes an MCP server for privileged actions like pulling code, and manages
task lifecycle including conversation archival and code retrieval.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "./orchestrator.yaml", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	rootCmd.AddCommand(taskCmd)
	rootCmd.AddCommand(logCmd)
	rootCmd.AddCommand(daemonCmd)
}
