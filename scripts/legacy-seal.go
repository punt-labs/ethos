//go:build ignore

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

func main() {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(githook.IsLegacyStandalone(body, hooks.SealIdent))
}
