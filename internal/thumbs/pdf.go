package thumbs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EnvPDFToPPM names pdftoppm when shfd thumbs gets no --pdftoppm.
const EnvPDFToPPM = "SHFD_PDFTOPPM"

// FindPDFToPPM resolves the pdftoppm to use: flag, else the environment
// value, else pdftoppm on PATH. "" when there is none; err says why.
func FindPDFToPPM(flag, env string) (string, error) {
	name := flag
	if name == "" {
		name = env
	}
	if name == "" {
		name = "pdftoppm"
	}
	return exec.LookPath(name)
}

// pdfFirstPage renders page 1 of the PDF on src (read from its start) at
// the largest size, with pdftoppm in its own process group, killed as a
// whole when Limits.PDFTimeout passes. The file reaches pdftoppm as its
// standard input, so it is the content that was hashed even if the path
// was replaced meanwhile.
func (g *Generator) pdfFirstPage(parent context.Context, src *os.File) (image.Image, error) {
	tmp, err := os.MkdirTemp("", "shfd-thumbs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	timeout := g.Limits.PDFTimeout
	if timeout <= 0 {
		timeout = DefaultPDFTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out := filepath.Join(tmp, "page")
	cmd := exec.CommandContext(ctx, g.PDFToPPM,
		"-f", "1", "-l", "1", "-singlefile", "-png",
		"-scale-to", strconv.Itoa(Sizes[0].Px), "-", out)
	cmd.Stdin = src
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp}
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if parent.Err() != nil {
		return nil, parent.Err() // shutting down: neither done nor failed
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, failf("pdftoppm timed out after %s", timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, failf("pdftoppm: %v: %s", err, msg)
	}
	f, err := os.Open(out + ".png")
	if err != nil {
		return nil, failf("pdftoppm wrote no page")
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, failf("pdftoppm output: %v", err)
	}
	return img, nil
}
