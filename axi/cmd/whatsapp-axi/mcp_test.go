package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Runs the real tool server on real pipes with the real command behind each
// tool, with no wasync anywhere: every reply must be one JSON-RPC line, and the
// commands' own output must come back as tool text rather than leaking into the
// protocol stream.
func TestToolServerOnRealPipesWithNoWasync(t *testing.T) {
	t.Setenv("WASYNC_BIN", "/nonexistent/wasync")
	t.Setenv("WHATSAPP_AXI_CONFIG_DIR", t.TempDir())
	t.Setenv("WASYNC_API_ADDR", "127.0.0.1:1")
	t.Setenv("WHATSAPP_AXI_TOKEN", "")

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveMCP(ctx) }()

	send := func(line string) { inW.WriteString(line + "\n") }
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whatsapp_status","arguments":{}}}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whatsapp_read_messages","arguments":{"chat":"x"}}}`)
	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	outW.Close()

	var got []map[string]any
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout carried something that is not JSON-RPC: %q", sc.Text())
		}
		got = append(got, m)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 replies, got %d: %v", len(got), got)
	}
	for _, m := range got[1:] {
		res := m["result"].(map[string]any)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("a tool returned no text: %v", m)
		}
	}
}
