package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// setupSignal cancels the provided cancel func on Ctrl-C (SIGINT) or SIGTERM.
func setupSignal(cancel func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
}