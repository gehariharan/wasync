//go:build !darwin && !windows

package notify

func send(title, text string) {}
