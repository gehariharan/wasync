package whatsapp

import (
	"io"
	"log"
	"log/slog"
	"strings"
	"sync/atomic"

	signallog "go.mau.fi/libsignal/logger"
)

// libraryLog is where output from third-party packages that keep a
// package-level logger ends up. It starts out discarding everything so that
// nothing reaches the terminal before a wasync logger exists.
var libraryLog atomic.Pointer[slog.Logger]

func init() {
	// libsignal, the Signal protocol package under whatsmeow, writes its log
	// lines to standard output unless a logger is installed. Install ours once
	// at start-up; Open only swaps the destination, so the package-level
	// logger is never written to while the library may be reading it.
	var sink signallog.Loggable = signalSink{}
	signallog.Setup(&sink)
	// Any package that falls back on the standard "log" package would
	// otherwise write to standard error.
	log.SetOutput(stdLogSink{})
}

// routeLibraryLogs sends third-party package logging into the given wasync
// logger. A nil logger discards it.
func routeLibraryLogs(l *slog.Logger) {
	libraryLog.Store(l)
}

// stopRoutingLibraryLogs discards third-party package logging again, unless
// it has since been routed to a different logger.
func stopRoutingLibraryLogs(l *slog.Logger) {
	libraryLog.CompareAndSwap(l, nil)
}

func libraryLogger() *slog.Logger {
	if l := libraryLog.Load(); l != nil {
		return l.With("component", "libsignal")
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// benignSignalMessages are libsignal complaints that happen during normal
// operation. They mean a copy of a message already handled (or one that arrived
// out of order) was received again; whatsmeow retries or ignores it and, when a
// message truly cannot be read, reports it separately as an undecryptable
// message.
var benignSignalMessages = []string{
	"old counter",
	"duplicate message",
	"message is a duplicate",
}

func isBenignSignalMessage(message string) bool {
	message = strings.ToLower(message)
	for _, fragment := range benignSignalMessages {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

// signalSink adapts libsignal's logger interface to slog. Debug and info lines
// are dropped because libsignal can describe key material at those levels.
type signalSink struct{}

func (signalSink) Debug(string, string) {}
func (signalSink) Info(string, string)  {}
func (signalSink) Warning(caller, message string) {
	libraryLogger().Warn(message, "caller", caller)
}
func (signalSink) Error(caller, message string) {
	if isBenignSignalMessage(message) {
		libraryLogger().Debug(message, "caller", caller)
		return
	}
	libraryLogger().Error(message, "caller", caller)
}
func (signalSink) Configure(string) {}

// stdLogSink receives output from the standard library "log" package.
type stdLogSink struct{}

func (stdLogSink) Write(p []byte) (int, error) {
	libraryLogger().Warn(strings.TrimSpace(string(p)), "caller", "log")
	return len(p), nil
}
