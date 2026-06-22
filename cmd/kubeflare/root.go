package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "kubeflare",
	Short: "Kubeflare control plane and Kubernetes API proxy",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "kubeflare: %v\n", err)
		os.Exit(1)
	}
}
