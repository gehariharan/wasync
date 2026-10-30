package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// cooldown keeps an agent from opening browser windows over and over.
const cooldown = 30 * time.Second

// Launcher opens the wasync setup page by running `wasync setup` in the
// background. It never passes a link or a credential and does not read what the
// program prints, so nothing the page needs to keep private reaches the agent.
type Launcher struct {
	// Bin finds the wasync program.
	Bin func() (string, error)
	// Spawn starts it detached from this process; nil uses the system.
	Spawn func(bin string, args []string) error
	Now   func() time.Time

	mu   sync.Mutex
	last time.Time
}

func (l *Launcher) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Start runs `wasync setup`. wasync opens the page itself: if it is already
// running it opens that copy's setup page and exits; otherwise it starts, shows
// the page and keeps running as the owner.
func (l *Launcher) Start(_ context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && l.now().Sub(l.last) < cooldown {
		return "Setup was opened a moment ago. Ask the person to look for the wasync page in their browser; it may be behind other windows.", nil
	}
	bin, err := l.Bin()
	if err != nil {
		return "", err
	}
	spawn := l.Spawn
	if spawn == nil {
		spawn = spawnDetached
	}
	if err := spawn(bin, []string{"setup"}); err != nil {
		return "", fmt.Errorf("could not start wasync setup: %w", err)
	}
	l.last = l.now()
	return "I opened the wasync setup page in the person's browser. They scan the QR code with WhatsApp on their phone (Settings, Linked devices), choose which chats to keep, and connect this agent. Tell them to finish there and then ask you to continue. If no page appeared, ask them to run `wasync setup`.", nil
}

// run starts the program with the given arguments and does not wait for it.
func run(bin string, args []string, configure func(*exec.Cmd)) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it if it exits so no zombie is left; the process itself outlives us.
	go func() { _ = cmd.Wait() }()
	return nil
}
