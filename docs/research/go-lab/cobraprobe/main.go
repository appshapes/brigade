// Cobra comparison: hello world with a two-level command tree on spf13/cobra.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{Use: "brigade", Short: "cobra size probe", Version: version, SilenceUsage: true, SilenceErrors: true}
	send := &cobra.Command{
		Use:  "send <session>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _ := cmd.Flags().GetString("reply-to")
			fmt.Println("send", args[0], r)
			return nil
		},
	}
	send.Flags().String("reply-to", "", "message id being answered")
	root.AddCommand(send)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
