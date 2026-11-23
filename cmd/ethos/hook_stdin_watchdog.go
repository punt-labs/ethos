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
	cmd := &cobra.Command{
		Use:    "stdin-watchdog <pid>",
		Short:  "Bound the lifetime of a shell stdin reader",
		Hidden: true,
		Args: func(cmd *cobra.Command, args []string) error {
			probe, _ := cmd.Flags().GetBool("probe")
			if probe {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		Run: func(cmd *cobra.Command, args []string) {
			probe, _ := cmd.Flags().GetBool("probe")
			if probe {
				return
			}
			pid, err := strconv.Atoi(args[0])
			if err != nil || pid <= 0 {
				return
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			hook.StdinWatchdog(ctx, pid, hook.InputTimeout)
		},
	}
	cmd.Flags().Bool("probe", false, "Check watchdog support without starting a timer")
	cmd.Flags().Bool("cancel", false, "With --probe, also check that post-edit stops its checks when signalled")
	hookCmd.AddCommand(cmd)
}
