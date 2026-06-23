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

// Execute 执行根命令，并在失败时输出清晰错误后退出。
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "kubeflare: %v\n", err)
		os.Exit(1)
	}
}
