package main

import (
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestHandleSignals(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	calls := make(chan string, 2)
	go handleSignals(sigs, func() { calls <- "cancel" }, func() { calls <- "kill" })
	next := func() string {
		select {
		case c := <-calls:
			return c
		case <-time.After(5 * time.Second):
			return "nothing"
		}
	}
	sigs <- syscall.SIGHUP
	first := next()
	sigs <- os.Interrupt
	got := []string{first, next()}
	if !slices.Equal(got, []string{"cancel", "kill"}) {
		t.Errorf("signals led to %v, want cancel, then kill", got)
	}
}
