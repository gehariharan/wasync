package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// A video goes to wasync as its bytes, never as a path: wasync does not open
// files for an agent. wasync-axi reads the file the agent named, with the
// agent's own permissions, and takes a preview frame with ffmpeg when it is
// installed, in this process rather than in the running wasync. wasync checks
// the video and decides whether it may be sent; the size check here only
// avoids posting a file wasync would refuse.
//
// wasync has its own copy of the ffmpeg helper (internal/whatsapp/video_frame.go):
// wasync-axi is a separate Go module and may not import wasync's internal
// packages (ADR-001), so the two cannot share one.

const (
	maxVideoBytes     = 64 << 20 // wasync's limit for a playable video
	maxThumbnailBytes = 64 << 10 // wasync's limit for the preview frame
)

// readAttachment reads a regular file of at most limit bytes.
func readAttachment(flag, path string, limit int64) ([]byte, error) {
	bad := func(format string, args ...any) error {
		return &contract.Error{Code: contract.CodeInvalidArgument, Message: fmt.Sprintf(flag+" "+format, args...)}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, bad("cannot be opened: %v", errors.Unwrap(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, bad("must be a regular file")
	}
	if info.Size() > limit {
		return nil, bad("is %.1f MiB; the most wasync sends is %d MiB", float64(info.Size())/(1<<20), limit>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, bad("cannot be read: %v", err)
	}
	if int64(len(data)) > limit {
		return nil, bad("is larger than %d MiB", limit>>20)
	}
	return data, nil
}

var errNoFFmpeg = errors.New("ffmpeg is not installed")

// ffmpegPaths are tried after PATH, because an agent host may not pass the
// shell's PATH on.
var ffmpegPaths = []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"}

// findFFmpeg is a test seam.
var findFFmpeg = func() (string, error) {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}
	for _, path := range ffmpegPaths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", errNoFFmpeg
}

// videoFrame asks ffmpeg for one representative frame of the video at path,
// as a JPEG at most 320 pixels on its longest side.
func videoFrame(ctx context.Context, path string) ([]byte, error) {
	ffmpeg, err := findFFmpeg()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The file: prefix keeps ffmpeg from reading a name as a URL or protocol.
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-v", "error", "-i", "file:"+abs,
		"-map", "0:v:0", "-frames:v", "1", "-vf", "thumbnail,scale='min(320,iw)':'min(320,ih)':force_original_aspect_ratio=decrease",
		"-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "5", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capped{buf: &out, limit: maxThumbnailBytes}, io.Discard
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg could not take a preview frame: %w", err)
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg returned no preview frame")
	}
	return out.Bytes(), nil
}

type capped struct {
	buf   *bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		return 0, errors.New("output too large")
	}
	return c.buf.Write(p)
}
