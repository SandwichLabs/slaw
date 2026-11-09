// Package cmd holds the Cobra command tree for the slaw CLI (workspace, query,
// import, export, conn, config, bundle, mcp, studio).
package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	logger  *slog.Logger
	// cliRedactor scrubs credential-shaped text from the log and from
	// workspace sessions' errors; the CLI resolves no secret values itself.
	cliRedactor = secrets.NewRedactor()

	// Version info set via ldflags.
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "slaw",
	Short:   "Analyze your data in a secure private workspace",
	Long:    "Slaw is a workspace for data that stays where it lives: connections to your databases and files, plus notebooks, queries and skills as plain files in a folder.\nStart with `slaw workspace init`, or `slaw studio --gui` for the desktop app.",
	Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(exitStatus(err))
	}
}

// exitStatus is the process exit status for a command error: the status the
// error carries (exitCodeError), otherwise 1.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

// exitCodeError is a command failure with its own exit status (`slaw
// semantics check` exits 2 when checks could not run but none failed). Its
// message is printed like any error.
type exitCodeError struct {
	code int
	msg  string
	err  error
}

func (e *exitCodeError) Error() string { return e.msg }
func (e *exitCodeError) Unwrap() error { return e.err }

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.slaw.yaml)")
	rootCmd.PersistentFlags().Bool("verbose", false, "enable verbose logging")
	_ = viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".slaw")
	}

	viper.SetEnvPrefix("SLAW")
	viper.AutomaticEnv()

	// Read config file if it exists; ignore "not found" errors.
	_ = viper.ReadInConfig()

	// Initialize logger.
	// A CLI prints its own messages: the structured log stays quiet unless
	// --verbose, so data commands do not spray JSON on a terminal.
	level := slog.LevelError
	if viper.GetBool("verbose") {
		level = slog.LevelDebug
	}
	logger = slog.New(secrets.NewRedactingHandler(
		slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}), cliRedactor))
}
