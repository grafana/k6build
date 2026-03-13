package clireadme

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Main updates the markdown documentation recursively based on cobra Command.
func Main(root *cobra.Command, headingOffset int) {
	exe := filepath.Base(os.Args[0]) //nolint:forbidigo
	if len(os.Args) != 2 {           //nolint:gomnd,forbidigo
		fmt.Fprintf(os.Stderr, "usage: %s filename", exe) //nolint:gosec,forbidigo
		os.Exit(1)                                        //nolint:forbidigo
	}

	if err := Update(root, os.Args[1], headingOffset); err != nil { //nolint:forbidigo
		fmt.Fprintf(os.Stderr, "%s: error: %s\n", exe, err) //nolint:gosec,forbidigo
		os.Exit(1)                                          //nolint:forbidigo
	}
}
