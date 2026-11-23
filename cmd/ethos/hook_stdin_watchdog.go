package main

import (
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:    "stdin-watchdog <pid>",
		Short:  "Bound the lifetime of a shell stdin reader",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			pid, err := strconv.Atoi(args[0])
			if err != nil || pid <= 0 {
				return
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			hook.StdinWatchdog(ctx, pid, hook.InputTimeout)
		},
	})
}
