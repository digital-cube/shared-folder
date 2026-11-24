package thumbs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"testing"

	"golang.org/x/image/bmp"
)

// decodeAvatar checks that out is a JPEG of AvatarPx × AvatarPx and
// returns it.
func decodeAvatar(t *testing.T, out []byte) image.Image {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != AvatarPx || cfg.Height != AvatarPx {
		t.Fatalf("avatar is %s %dx%d (%v), want a jpeg of %dx%d", format, cfg.Width, cfg.Height, err, AvatarPx, AvatarPx)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r>>8 > 200 && g>>8 < 60 && b>>8 < 60
}

func isBlue(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r>>8 < 60 && g>>8 < 60 && b>>8 > 200
}

// Every accepted format gives a square JPEG, GIF its first frame; small
// pictures are enlarged.
func TestAvatarFormats(t *testing.T) {
	src := gradient(600, 400)
	webp, err := os.ReadFile("testdata/gradient-300x150.webp")
	if err != nil {
		t.Fatal(err)
	}
	// An animated GIF: a blue first frame, a red second one.
	frame := func(c color.Color) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 80, 60), color.Palette{color.RGBA{0, 0, 255, 255}, color.RGBA{255, 0, 0, 255}})
		for i := range p.Pix {
			if c == color.Black {
				p.Pix[i] = 1
			}
		}
		return p
	}
	anim := encode(t, func(w io.Writer) error {
		return gif.EncodeAll(w, &gif.GIF{Image: []*image.Paletted{frame(color.White), frame(color.Black)}, Delay: []int{10, 10}})
	})
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"png", encode(t, func(w io.Writer) error { return png.Encode(w, src) })},
		{"jpeg", encode(t, func(w io.Writer) error { return jpeg.Encode(w, src, nil) })},
		{"webp", webp},
		{"gif", anim},
		{"small png", encode(t, func(w io.Writer) error { return png.Encode(w, gradient(40, 30)) })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Avatar(tc.data, AvatarMaxPixels)
			if err != nil {
				t.Fatal(err)
			}
			img := decodeAvatar(t, out)
			if tc.name == "gif" && !isBlue(img.At(AvatarPx/2, AvatarPx/2)) {
				t.Errorf("gif: not the first frame: %v", img.At(AvatarPx/2, AvatarPx/2))
			}
		})
	}
}

// What is not a PNG, JPEG, WebP or GIF by its content is refused, whatever
// it is called: an HTML page, a BMP and a truncated PNG.
func TestAvatarRefusesOtherContent(t *testing.T) {
	pngData := encode(t, func(w io.Writer) error { return png.Encode(w, gradient(100, 100)) })
	for name, data := range map[string][]byte{
		"html": []byte("<!doctype html><html><script>alert(1)</script></html>"),
		"bmp":  encode(t, func(w io.Writer) error { return bmp.Encode(w, gradient(50, 50)) }),
		"cut":  pngData[:len(pngData)/2],
		"none": nil,
	} {
		if _, err := Avatar(data, AvatarMaxPixels); !errors.Is(err, ErrAvatarFormat) {
			t.Errorf("%s: %v, want ErrAvatarFormat", name, err)
		}
	}
}

// bombPNG is a valid PNG header that claims w×h pixels, with no pixels
// behind it: what a decompression bomb looks like before it is decoded.
func bombPNG(t *testing.T, w, h uint32) []byte {
	data := encode(t, func(wr io.Writer) error { return png.Encode(wr, image.NewGray(image.Rect(0, 0, 1, 1))) })
	// IHDR follows the 8-byte signature: length(4) "IHDR"(4) width height ...
	binary.BigEndian.PutUint32(data[16:], w)
	binary.BigEndian.PutUint32(data[20:], h)
	binary.BigEndian.PutUint32(data[29:], crc32.ChecksumIEEE(data[12:29]))
	return data
}

func TestAvatarDecompressionBomb(t *testing.T) {
	bomb := bombPNG(t, 50_000, 50_000) // 2.5 gigapixels in a few dozen bytes
	if _, err := Avatar(bomb, AvatarMaxPixels); !errors.Is(err, ErrAvatarPixels) {
		t.Fatalf("bomb: %v, want ErrAvatarPixels", err)
	}
	// Just over the limit is refused too; the limit is read from the header.
	if _, err := Avatar(bombPNG(t, 8000, 5001), AvatarMaxPixels); !errors.Is(err, ErrAvatarPixels) {
		t.Errorf("40 MP + 1 row: %v", err)
	}
}

// The middle square is kept: a 300x100 picture with a blue middle third
// and red sides comes out all blue.
func TestAvatarCropsTheMiddle(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 100 && x < 200 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	for _, src := range []image.Image{img, rotate90(img)} {
		out, err := Avatar(encode(t, func(w io.Writer) error { return png.Encode(w, src) }), AvatarMaxPixels)
		if err != nil {
			t.Fatal(err)
		}
		a := decodeAvatar(t, out)
		for _, p := range []image.Point{{8, 8}, {AvatarPx - 8, 8}, {AvatarPx / 2, AvatarPx / 2}, {8, AvatarPx - 8}, {AvatarPx - 8, AvatarPx - 8}} {
			if !isBlue(a.At(p.X, p.Y)) {
				t.Errorf("%v of a %v picture is %v, want blue", p, src.Bounds().Size(), a.At(p.X, p.Y))
			}
		}
	}
}

func rotate90(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(y, x, src.At(x, y))
		}
	}
	return dst
}

// A phone photo is turned upright, and the result carries no EXIF (nor
// any other metadata): the stored square has its top-left quarter red.
func TestAvatarEXIFOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < 100 && y < 100 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	plain := encode(t, func(w io.Writer) error { return jpeg.Encode(w, img, &jpeg.Options{Quality: 95}) })
	q := AvatarPx / 4
	for _, tc := range []struct {
		orient uint16
		bo     binary.ByteOrder
		red    image.Point // the quarter that is red when viewed
	}{
		{1, binary.BigEndian, image.Pt(q, q)},
		{3, binary.LittleEndian, image.Pt(3*q, 3*q)},
		{6, binary.BigEndian, image.Pt(3*q, q)},
		{8, binary.LittleEndian, image.Pt(q, 3*q)},
	} {
		data := withEXIF(plain, tc.orient, tc.bo)
		out, err := Avatar(data, AvatarMaxPixels)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("Exif\x00\x00")) {
			t.Errorf("orientation %d: the avatar keeps EXIF", tc.orient)
		}
		a := decodeAvatar(t, out)
		for _, p := range []image.Point{{q, q}, {3 * q, q}, {q, 3 * q}, {3 * q, 3 * q}} {
			if isRed(a.At(p.X, p.Y)) != (p == tc.red) {
				t.Errorf("orientation %d: quarter at %v red=%v", tc.orient, p, !(p == tc.red))
			}
		}
	}
}
