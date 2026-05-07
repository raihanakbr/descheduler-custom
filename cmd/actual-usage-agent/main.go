package main

import (
	"context"
	"os"

	"sigs.k8s.io/descheduler/pkg/actualusageagent"
)

func main() {
	if err := actualusageagent.NewCommand().ExecuteContext(context.Background()); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
