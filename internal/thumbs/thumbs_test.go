package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// run makes the thumbnail of data with g into a fresh store.
func run(t *testing.T, g *Generator, data []byte, hash string, kind Kind) (Store, Result) {
	t.Helper()
	st := Store{Dir: filepath.Join(t.TempDir(), Dir)}
	p := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if hash == "" {
		hash = sum(data)
	}
	res, err := g.Generate(context.Background(), st, f, hash, kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.workPath(hash)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".work marker left behind: %v", err)
	}
	return st, res
}

func gen() *Generator { return &Generator{Limits: DefaultLimits()} }

// sizes decodes every size of hash and returns their dimensions, l, m, s.
func sizes(t *testing.T, st Store, hash string) [][2]int {
	t.Helper()
	var out [][2]int
	for _, s := range Sizes {
		img := load(t, st, hash, s.Name)
		out = append(out, [2]int{img.Bounds().Dx(), img.Bounds().Dy()})
	}
	return out
}

func load(t *testing.T, st Store, hash, size string) image.Image {
	t.Helper()
	f, err := os.Open(st.Path(hash, size))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("%s.%s: %v", hash[:8], size, err)
	}
	return img
}

func gradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func encode(t *testing.T, f func(io.Writer) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := f(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestKindOf(t *testing.T) {
	for name, want := range map[string]Kind{
		"a.jpg": KindImage, "A.JPEG": KindImage, "x/y.Png": KindImage, "g.gif": KindImage,
		"w.webp": KindImage, "b.BMP": KindImage, "t.tif": KindImage, "t.tiff": KindImage,
		"doc.PDF": KindPDF, "a.txt": "", "jpg": "", "a.jpg.txt": "", "a.svg": "", "a.heic": "", "": "",
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
	for _, h := range []string{strings.Repeat("a", 63), strings.Repeat("A", 64), "../" + strings.Repeat("a", 61), strings.Repeat("g", 64)} {
		if ValidHash(h) {
			t.Errorf("ValidHash(%q)", h)
		}
	}
	if !ValidHash(strings.Repeat("0f", 32)) {
		t.Error("ValidHash rejects a hash")
	}
}

// Every image format, scaled to every size with its proportions.
func TestFormats(t *testing.T) {
	src := gradient(1000, 500)
	pal := image.NewPaletted(src.Bounds(), color.Palette{color.White, color.Black, color.RGBA{255, 0, 0, 255}})
	for y := 0; y < 500; y++ {
		for x := 0; x < 1000; x++ {
			pal.SetColorIndex(x, y, uint8((x/50+y/50)%3))
		}
	}
	webp, err := os.ReadFile("testdata/gradient-300x150.webp")
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int{{512, 256}, {256, 128}, {96, 48}}
	for _, tc := range []struct {
		name string
		data []byte
		want [][2]int
	}{
		{"png", encode(t, func(w io.Writer) error { return png.Encode(w, src) }), want},
		{"jpeg", encode(t, func(w io.Writer) error { return jpeg.Encode(w, src, nil) }), want},
		{"gif", encode(t, func(w io.Writer) error {
			return gif.EncodeAll(w, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{10, 10}})
		}), want},
		{"bmp", encode(t, func(w io.Writer) error { return bmp.Encode(w, src) }), want},
		{"tiff", encode(t, func(w io.Writer) error { return tiff.Encode(w, src, nil) }), want},
		// 300x150 is under l: not enlarged.
		{"webp", webp, [][2]int{{300, 150}, {256, 128}, {96, 48}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, res := run(t, gen(), tc.data, "", KindImage)
			if res.Outcome != Ready {
				t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
			}
			h := sum(tc.data)
			if got := sizes(t, st, h); !equal(got, tc.want) {
				t.Errorf("sizes %v, want %v", got, tc.want)
			}
			if !st.Ready(h) || st.Failed(h) {
				t.Error("not ready")
			}
		})
	}
}

func equal(a, b [][2]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestProportionsAndNoUpscale(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want [][2]int
	}{
		{60, 40, [][2]int{{60, 40}, {60, 40}, {60, 40}}},         // small: never enlarged
		{1, 1, [][2]int{{1, 1}, {1, 1}, {1, 1}}},                 // one pixel
		{200, 1000, [][2]int{{102, 512}, {51, 256}, {19, 96}}},   // portrait
		{4000, 100, [][2]int{{512, 13}, {256, 6}, {96, 2}}},      // panorama, box-filtered first
		{2000, 1, [][2]int{{512, 1}, {256, 1}, {96, 1}}},         // never 0 px
		{256, 300, [][2]int{{256, 300}, {218, 256}, {82, 96}}},   // between sizes
		{1500, 1000, [][2]int{{512, 341}, {256, 171}, {96, 64}}}, // rounding
		{97, 96, [][2]int{{97, 96}, {97, 96}, {96, 95}}},         // just over s
		{3000, 3000, [][2]int{{512, 512}, {256, 256}, {96, 96}}}, // box factor 2
		{5000, 2500, [][2]int{{512, 256}, {256, 128}, {96, 48}}}, // box factor 4
	} {
		img := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
		data := encode(t, func(w io.Writer) error { return png.Encode(w, img) })
		st, res := run(t, gen(), data, "", KindImage)
		if res.Outcome != Ready {
			t.Fatalf("%dx%d: %s %s", tc.w, tc.h, res.Outcome, res.Reason)
		}
		if got := sizes(t, st, sum(data)); !equal(got, tc.want) {
			t.Errorf("%dx%d: sizes %v, want %v", tc.w, tc.h, got, tc.want)
		}
	}
}

// Transparent pixels are composited onto white.
func TestTransparencyOnWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			switch {
			case x < 100: // fully transparent (its colour must not show)
				img.Set(x, y, color.NRGBA{0, 0, 0, 0})
			case x < 200: // half-transparent black: grey
				img.Set(x, y, color.NRGBA{0, 0, 0, 128})
			default: // opaque black
				img.Set(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	for _, k := range []int{1, 8} { // as it is, and box-filtered
		src := img
		if k > 1 {
			src = image.NewNRGBA(image.Rect(0, 0, 300*k, 100*k))
			for y := 0; y < 100*k; y++ {
				for x := 0; x < 300*k; x++ {
					src.Set(x, y, img.At(x/k, y/k))
				}
			}
		}
		data := encode(t, func(w io.Writer) error { return png.Encode(w, src) })
		st, res := run(t, gen(), data, "", KindImage)
		if res.Outcome != Ready {
			t.Fatalf("k=%d: %s %s", k, res.Outcome, res.Reason)
		}
		l := load(t, st, sum(data), "l")
		b := l.Bounds()
		for _, c := range []struct {
			x    int
			want uint8
		}{{b.Dx() / 6, 255}, {b.Dx() / 2, 127}, {b.Dx() * 5 / 6, 0}} {
			r, g, bl, _ := l.At(c.x, b.Dy()/2).RGBA()
			for _, v := range []uint32{r >> 8, g >> 8, bl >> 8} {
				if d := int(v) - int(c.want); d < -6 || d > 6 {
					t.Errorf("k=%d x=%d: %d, want about %d", k, c.x, v, c.want)
				}
			}
		}
	}
}

// withEXIF inserts an APP1 Exif segment with the orientation tag after
// the SOI marker of a JPEG.
func withEXIF(jpg []byte, orient uint16, bo binary.ByteOrder) []byte {
	var t bytes.Buffer
	if bo == binary.LittleEndian {
		t.WriteString("II")
	} else {
		t.WriteString("MM")
	}
	binary.Write(&t, bo, uint16(42))
	binary.Write(&t, bo, uint32(8))
	binary.Write(&t, bo, uint16(2)) // two entries, orientation second
	binary.Write(&t, bo, uint16(0x010F))
	binary.Write(&t, bo, uint16(2))
	binary.Write(&t, bo, uint32(4))
	t.WriteString("ACM\x00")
	binary.Write(&t, bo, uint16(0x0112))
	binary.Write(&t, bo, uint16(3))
	binary.Write(&t, bo, uint32(1))
	binary.Write(&t, bo, orient)
	binary.Write(&t, bo, uint16(0))
	binary.Write(&t, bo, uint32(0))
	seg := append([]byte("Exif\x00\x00"), t.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// A photo is shown upright: the stored image is 200x100 with its top-left
// quarter red, and the orientation says where that corner is to be seen.
func TestEXIFOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < 100 && y < 50 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	plain := encode(t, func(w io.Writer) error { return jpeg.Encode(w, img, &jpeg.Options{Quality: 95}) })
	for _, tc := range []struct {
		orient uint16
		bo     binary.ByteOrder
		w, h   int
		redX   float64 // centre of the red quarter, as a fraction of the size
		redY   float64
	}{
		{1, binary.BigEndian, 200, 100, 0.25, 0.25},
		{3, binary.LittleEndian, 200, 100, 0.75, 0.75},
		{6, binary.BigEndian, 100, 200, 0.75, 0.25},
		{8, binary.LittleEndian, 100, 200, 0.25, 0.75},
	} {
		data := withEXIF(plain, tc.orient, tc.bo)
		if got := exifOrientation(data); got != int(tc.orient) {
			t.Fatalf("orientation %d read as %d", tc.orient, got)
		}
		st, res := run(t, gen(), data, "", KindImage)
		if res.Outcome != Ready {
			t.Fatalf("orientation %d: %s %s", tc.orient, res.Outcome, res.Reason)
		}
		l := load(t, st, sum(data), "l")
		if b := l.Bounds(); b.Dx() != tc.w || b.Dy() != tc.h {
			t.Errorf("orientation %d: %dx%d, want %dx%d", tc.orient, b.Dx(), b.Dy(), tc.w, tc.h)
			continue
		}
		red := func(fx, fy float64) bool {
			r, _, b, _ := l.At(int(fx*float64(tc.w)), int(fy*float64(tc.h))).RGBA()
			return r>>8 > 200 && b>>8 < 60
		}
		for _, q := range [][2]float64{{0.25, 0.25}, {0.75, 0.25}, {0.25, 0.75}, {0.75, 0.75}} {
			want := q[0] == tc.redX && q[1] == tc.redY
			if red(q[0], q[1]) != want {
				t.Errorf("orientation %d: quarter %v red=%v, want %v", tc.orient, q, !want, want)
			}
		}
	}
	if got := exifOrientation(plain); got != 1 {
		t.Errorf("no EXIF: orientation %d", got)
	}
	if got := exifOrientation([]byte("not a jpeg")); got != 1 {
		t.Errorf("garbage: orientation %d", got)
	}
}

func checkFailed(t *testing.T, st Store, hash string, res Result, reason string) {
	t.Helper()
	if res.Outcome != Failed {
		t.Fatalf("outcome %s, want failed", res.Outcome)
	}
	got, err := st.FailReason(hash)
	if err != nil {
		t.Fatalf("no .fail marker: %v", err)
	}
	if !strings.Contains(got, reason) || got != res.Reason {
		t.Errorf(".fail says %q (result %q), want %q", got, res.Reason, reason)
	}
	if st.Ready(hash) {
		t.Error("ready despite failing")
	}
	for _, s := range Sizes {
		if _, err := os.Stat(st.Path(hash, s.Name)); err == nil {
			t.Errorf("size %s written", s.Name)
		}
	}
	_, failed, _ := st.Scan()
	if !failed[hash] {
		t.Error("Scan does not see the .fail marker")
	}
}

func TestLimits(t *testing.T) {
	data := encode(t, func(w io.Writer) error { return png.Encode(w, gradient(20, 20)) })
	g := gen()
	g.Limits.MaxPixels = 399
	st, res := run(t, g, data, "", KindImage)
	checkFailed(t, st, sum(data), res, "20x20 pixels is over the limit of 399")

	g = gen()
	g.Limits.MaxImageBytes = int64(len(data)) - 1
	st, res = run(t, g, data, "", KindImage)
	checkFailed(t, st, sum(data), res, "image larger than")

	g = gen()
	g.Limits.MaxImageBytes = int64(len(data)) // exactly the limit is fine
	if _, res := run(t, g, data, "", KindImage); res.Outcome != Ready {
		t.Errorf("at the limit: %s %s", res.Outcome, res.Reason)
	}
}

func init() {
	// A format whose decoder panics, to prove a panic becomes a .fail.
	image.RegisterFormat("panicky", "PANICKY", func(io.Reader) (image.Image, error) {
		panic("boom")
	}, func(io.Reader) (image.Config, error) {
		return image.Config{ColorModel: color.RGBAModel, Width: 2, Height: 2}, nil
	})
}

func TestBrokenSources(t *testing.T) {
	good := encode(t, func(w io.Writer) error { return png.Encode(w, gradient(50, 50)) })
	for _, tc := range []struct {
		name, data, reason string
	}{
		{"garbage", "this is not an image at all", "not a readable image"},
		{"truncated png", string(good[:len(good)/2]), "png:"},
		{"empty-looking jpeg", "\xFF\xD8\xFF\xD9", "not a readable image"},
		{"decoder panic", "PANICKY data", "decoder panic: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, res := run(t, gen(), []byte(tc.data), "", KindImage)
			checkFailed(t, st, sum([]byte(tc.data)), res, tc.reason)
		})
	}
}

// A file that changed after the job was handed out is skipped: no
// thumbnail, no marker.
func TestHashMismatchSkips(t *testing.T) {
	data := encode(t, func(w io.Writer) error { return png.Encode(w, gradient(50, 50)) })
	other := sum([]byte("the content the job was about"))
	st, res := run(t, gen(), data, other, KindImage)
	if res.Outcome != Skipped {
		t.Fatalf("outcome %s", res.Outcome)
	}
	if hs, _ := st.Hashes(); len(hs) != 0 {
		t.Errorf("left %v", hs)
	}
	des, _ := os.ReadDir(st.sub(other))
	for _, de := range des {
		t.Errorf("left %s", de.Name())
	}
	// Even over a limit: the mismatch wins, no .fail for the job's hash.
	g := gen()
	g.Limits.MaxImageBytes = 10
	if _, res := run(t, g, data, other, KindImage); res.Outcome != Skipped {
		t.Errorf("over the limit: %s", res.Outcome)
	}
}

func TestStoreMarkers(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), Dir)}
	if r, f, err := st.Scan(); err != nil || len(r)+len(f) != 0 {
		t.Fatalf("missing directory: %v %v %v", r, f, err)
	}
	h1, h2, h3 := strings.Repeat("a1", 32), strings.Repeat("b2", 32), strings.Repeat("c3", 32)
	for _, h := range []string{h1, h2, h3} {
		os.MkdirAll(st.sub(h), 0o700)
	}
	// h1 crashed mid-way, with l written; h2 is ready; h3 failed.
	os.WriteFile(st.workPath(h1), nil, 0o600)
	os.WriteFile(st.Path(h1, "l"), []byte("x"), 0o600)
	for _, s := range Sizes {
		os.WriteFile(st.Path(h2, s.Name), []byte("x"), 0o600)
	}
	os.WriteFile(st.failPath(h3), []byte("bad\n"), 0o600)
	os.WriteFile(filepath.Join(st.sub(h2), "."+h2+".s.jpg.123"), nil, 0o600) // temp file
	os.WriteFile(filepath.Join(st.sub(h2), "notes.txt"), nil, 0o600)         // ignored

	got, err := st.RecoverWork()
	if err != nil || len(got) != 1 || got[0] != h1 {
		t.Fatalf("RecoverWork = %v, %v", got, err)
	}
	if r, _ := st.FailReason(h1); r != CrashReason {
		t.Errorf("h1 .fail = %q", r)
	}
	if _, err := os.Stat(st.Path(h1, "l")); err == nil {
		t.Error("partial l of h1 kept")
	}
	ready, failed, err := st.Scan()
	if err != nil || len(ready) != 1 || !ready[h2] || len(failed) != 2 || !failed[h1] || !failed[h3] {
		t.Errorf("Scan = %v %v %v", ready, failed, err)
	}
	hs, _ := st.Hashes()
	if len(hs) != 3 {
		t.Errorf("Hashes = %v", hs)
	}
	if n, err := st.RemoveStaleTemp(time.Hour, time.Now().Add(2*time.Hour)); err != nil || n != 1 {
		t.Errorf("RemoveStaleTemp = %d %v", n, err)
	}
	if n, err := st.ClearFailed(); err != nil || n != 2 {
		t.Errorf("ClearFailed = %d %v", n, err)
	}
	if err := st.Remove(h2); err != nil {
		t.Fatal(err)
	}
	if ready, failed, _ := st.Scan(); len(ready)+len(failed) != 0 {
		t.Errorf("after clear and remove: %v %v", ready, failed)
	}
}

func TestPDF(t *testing.T) {
	tool, err := FindPDFToPPM("", "")
	if err != nil {
		t.Skip("pdftoppm not installed (apt install poppler-utils)")
	}
	data, err := os.ReadFile("testdata/halfred-200x100.pdf")
	if err != nil {
		t.Fatal(err)
	}
	g := gen()
	g.PDFToPPM = tool
	st, res := run(t, g, data, "", KindPDF)
	if res.Outcome != Ready {
		t.Fatalf("%s %s", res.Outcome, res.Reason)
	}
	h := sum(data)
	if got := sizes(t, st, h); !equal(got, [][2]int{{512, 256}, {256, 128}, {96, 48}}) {
		t.Errorf("sizes %v", got)
	}
	// The page is 200x100 pt with its left half red.
	l := load(t, st, h, "l")
	if r, g, b, _ := l.At(128, 128).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("left half not red: %d %d %d", r>>8, g>>8, b>>8)
	}
	if r, g, b, _ := l.At(384, 128).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("right half not white: %d %d %d", r>>8, g>>8, b>>8)
	}

	// Over the limit, and not a PDF.
	g.Limits.MaxPDFBytes = int64(len(data)) - 1
	st, res = run(t, g, data, "", KindPDF)
	checkFailed(t, st, h, res, "PDF larger than")
	g.Limits = DefaultLimits()
	st, res = run(t, g, []byte("%PDF-1.4 broken"), "", KindPDF)
	checkFailed(t, st, sum([]byte("%PDF-1.4 broken")), res, "pdftoppm")
}

// A pdftoppm that hangs is killed with its whole process group when the
// time is up, and the PDF gets a .fail.
func TestPDFTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "pdftoppm")
	// sleep runs as a child of sh: killing only sh would leave it holding
	// stderr, and Run would wait for it.
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nsleep 30\necho done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g := gen()
	g.PDFToPPM = tool
	g.Limits.PDFTimeout = 300 * time.Millisecond
	data := []byte("%PDF-1.4 whatever")
	start := time.Now()
	st, res := run(t, g, data, "", KindPDF)
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("took %s: the process group was not killed", took)
	}
	checkFailed(t, st, sum(data), res, "pdftoppm timed out after 300ms")
}
