package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// The preview frame of a video needs a video decoder, which wasync does not
// carry. When ffmpeg is installed, the command that has the file (wasync send)
// asks it for one representative frame; without it the message gets a plain
// preview and the video still plays. ffmpeg runs in that command's process on
// a file the person named, never inside the running owner on bytes an agent
// sent.
//
// wasync-axi has its own copy of this helper (axi/internal/cli/video.go):
// it is a separate Go module that may not import wasync's internal packages
// (ADR-001), so the two cannot share one.

// ErrNoFFmpeg means ffmpeg is not installed, so no frame could be taken.
var ErrNoFFmpeg = errors.New("ffmpeg is not installed")

// ffmpegPaths are tried after PATH, because a service or app launched by the
// system may not inherit the shell's PATH.
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
	return "", ErrNoFFmpeg
}

// VideoFrame asks ffmpeg for a representative frame of the video at path, as
// a JPEG no larger than the preview. It returns ErrNoFFmpeg when ffmpeg is
// not installed.
func VideoFrame(ctx context.Context, path string) ([]byte, error) {
	ffmpeg, err := findFFmpeg()
	if err != nil {
		return nil, err
	}
	// The file: prefix keeps ffmpeg from reading a name as a URL or protocol.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	size := strconv.Itoa(thumbnailMaxDim)
	// thumbnail picks a typical frame from the first ones rather than a black
	// first frame; scale shrinks it to the preview's size, keeping its shape.
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-v", "error", "-i", "file:"+abs,
		"-map", "0:v:0", "-frames:v", "1", "-vf", "thumbnail,scale='min("+size+",iw)':'min("+size+",ih)':force_original_aspect_ratio=decrease",
		"-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "5", "pipe:1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitedBuffer{buf: &out, limit: MaxThumbnailBytes}, &limitedBuffer{buf: &stderr, limit: 4 << 10}
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg could not take a preview frame: %v %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg returned no preview frame")
	}
	return out.Bytes(), nil
}

// limitedBuffer keeps at most limit bytes and reports an error past it, which
// stops the command rather than growing without bound.
type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, errors.New("output too large")
	}
	return b.buf.Write(p)
}
