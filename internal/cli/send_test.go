package cli

import (
	"bytes"
	"context"
	"errors"
	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/storage"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

type fakeSendingClient struct {
	fakeClient
	sent    []whatsapp.SendInput
	sendErr error
}

func (f *fakeSendingClient) Send(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
	f.sent = append(f.sent, in)
	status := "server_acknowledged"
	if f.sendErr != nil {
		status = "unknown"
	}
	return whatsapp.SendResult{ChatID: in.To, MessageID: "synthetic-send", Status: status}, f.sendErr
}
func TestExplicitSendIsSingleAttemptAndClosesSession(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		var output bytes.Buffer
		client := &fakeSendingClient{}
		if ambiguous {
			client.sendErr = errors.New("ambiguous acknowledgement")
		}
		app := &App{Out: &output, Err: &output, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil }}
		err := app.Run(context.Background(), []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "send", "--to", "one@g.us", "--text", "synthetic"})
		if (err != nil) != ambiguous || len(client.sent) != 1 || !client.closed || !client.connect {
			t.Fatalf("send lifecycle: %+v %v", client, err)
		}
	}
}
func TestInvalidSendDoesNotOpenDevice(t *testing.T) {
	var output bytes.Buffer
	app := &App{Out: &output, Err: &output, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("invalid send opened a device")
		return nil, nil
	}}
	if err := app.Run(context.Background(), []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "send", "--to", "status@broadcast", "--text", "no"}); err == nil {
		t.Fatal("broadcast destination accepted")
	}
}

func TestSendReusesOwnerAndCleanupOnlyQueuesApproval(t *testing.T) {
	app, output, store, account, dir := queryFixture(t)
	queue := &approval.Queue{}
	var calls atomic.Int32
	server, err := api.StartWithApprovals("127.0.0.1:0", dir, store, nil, queue, func(ctx context.Context, _ whatsapp.SendInput) (whatsapp.SendResult, error) {
		if _, limited := ctx.Deadline(); limited {
			t.Error("owner imposed a short send deadline")
		}
		calls.Add(1)
		return whatsapp.SendResult{Status: "server_acknowledged", MessageID: "fake"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("opened a competing client")
		return nil, nil
	}
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	if err = app.Run(context.Background(), append(flags, "send", "--to", "one@g.us", "--text", "synthetic")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("owner did not receive exactly one send")
	}
	output.Reset()
	if err = app.Run(context.Background(), append(flags, "clean", "--chat", "one@g.us", "--all")); err != nil {
		t.Fatal(err)
	}
	if queue.Pending(account) == nil {
		t.Fatal("cleanup request not queued")
	}
	page, err := store.Messages(context.Background(), account, "one@g.us", storage.MessageQuery{Limit: 5})
	if err != nil || len(page.Items) != 3 {
		t.Fatal("agent cleanup changed data without human approval")
	}
	if err = app.Run(context.Background(), append(flags, "clean", "--chat", "one@g.us", "--all", "--yes")); err == nil {
		t.Fatal("noninteractive bypass accepted")
	}
}

func videoFixture(name string) string {
	return filepath.Join("..", "whatsapp", "testdata", "video", name)
}

func TestSendVideoCarriesItsPreview(t *testing.T) {
	thumb := filepath.Join(t.TempDir(), "preview.png")
	img := image.NewGray(image.Rect(0, 0, 640, 360))
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumb, pngData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--thumbnail", thumb}, nil} {
		var output, diagnostics bytes.Buffer
		client := &fakeSendingClient{}
		app := &App{Out: &output, Err: &diagnostics, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil }}
		args := append([]string{"--data-dir", filepath.Join(t.TempDir(), "data"), "send", "--to", "one@g.us", "--file", videoFixture("h264-aac.mp4"), "--type", "video", "--text", "Watch"}, extra...)
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		if len(client.sent) != 1 {
			t.Fatal("not sent once")
		}
		in := client.sent[0]
		if in.Type != "video" || !filepath.IsAbs(in.File) || in.Text != "Watch" {
			t.Fatalf("%+v", in)
		}
		if len(in.Thumbnail) == 0 {
			// Only a computer without ffmpeg sends no preview, and it says so.
			if extra != nil || !strings.Contains(diagnostics.String(), "plain preview") {
				t.Fatalf("no preview: %q", diagnostics.String())
			}
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(in.Thumbnail))
		if err != nil || cfg.Width > 320 || cfg.Height > 320 {
			t.Fatalf("preview %+v %v", cfg, err)
		}
	}
}

func TestUnplayableVideoIsRefusedBeforeConnecting(t *testing.T) {
	var output bytes.Buffer
	app := &App{Out: &output, Err: &output, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("refused video opened a device")
		return nil, nil
	}}
	data := filepath.Join(t.TempDir(), "data")
	for name, args := range map[string][]string{
		"hevc":                {"--file", videoFixture("hevc.mp4"), "--type", "video"},
		"quicktime":           {"--file", videoFixture("quicktime.mov"), "--type", "video"},
		"thumbnail for image": {"--file", videoFixture("h264-aac.mp4"), "--type", "image", "--thumbnail", videoFixture("h264-aac.mp4")},
		"thumbnail not image": {"--file", videoFixture("h264-aac.mp4"), "--type", "video", "--thumbnail", videoFixture("h264-aac.mp4")},
		"missing file":        {"--file", filepath.Join(data, "absent.mp4"), "--type", "video"},
	} {
		if err := app.Run(context.Background(), append([]string{"--data-dir", data, "send", "--to", "one@g.us"}, args...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSendVideoThroughOwnerKeepsThePreview(t *testing.T) {
	app, _, store, _, dir := queryFixture(t)
	var got whatsapp.SendInput
	server, err := api.StartWithApprovals("127.0.0.1:0", dir, store, nil, &approval.Queue{}, func(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
		got = in
		return whatsapp.SendResult{Status: "server_acknowledged", MessageID: "fake"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("opened a competing client")
		return nil, nil
	}
	thumb := filepath.Join(t.TempDir(), "preview.png")
	var pngData bytes.Buffer
	if err = png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 64, 36))); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumb, pngData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	if err = app.Run(context.Background(), append(flags, "send", "--to", "one@g.us", "--file", videoFixture("h264-aac.mp4"), "--type", "video", "--thumbnail", thumb)); err != nil {
		t.Fatal(err)
	}
	if got.Type != "video" || !filepath.IsAbs(got.File) || len(got.Thumbnail) == 0 {
		t.Fatalf("%+v", got)
	}
	if _, err = jpeg.DecodeConfig(bytes.NewReader(got.Thumbnail)); err != nil {
		t.Fatal(err)
	}
}
