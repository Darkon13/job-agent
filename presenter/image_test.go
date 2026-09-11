package presenter

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
)

func testPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func TestParseProtocolAndDetect(t *testing.T) {
	for value, want := range map[string]Protocol{
		"":        ProtocolAuto,
		"auto":    ProtocolAuto,
		"kitty":   ProtocolKitty,
		"sixel":   ProtocolSixel,
		"unicode": ProtocolUnicode,
		"file":    ProtocolFile,
	} {
		got, err := ParseProtocol(value)
		if err != nil || got != want {
			t.Fatalf("ParseProtocol(%q) = %q err=%v", value, got, err)
		}
	}
	if _, err := ParseProtocol("bmp"); err == nil {
		t.Fatal("expected unknown protocol to fail")
	}
	if got := DetectProtocol(ProtocolAuto, map[string]string{"KITTY_WINDOW_ID": "1"}); got != ProtocolKitty {
		t.Fatalf("kitty detection = %q", got)
	}
	if got := DetectProtocol(ProtocolAuto, map[string]string{"TERM": "xterm-sixel"}); got != ProtocolSixel {
		t.Fatalf("sixel detection = %q", got)
	}
	if got := DetectProtocol(ProtocolAuto, map[string]string{"TERM": "xterm-256color"}); got != ProtocolUnicode {
		t.Fatalf("fallback detection = %q", got)
	}
	if got := DetectProtocol(ProtocolFile, map[string]string{"KITTY_WINDOW_ID": "1"}); got != ProtocolFile {
		t.Fatalf("explicit protocol = %q", got)
	}
}

func TestRenderKittyChunksLargePayloads(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	random := rand.New(rand.NewPCG(1, 2))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(random.UintN(256)), G: uint8(random.UintN(256)), B: uint8(random.UintN(256)), A: 255})
		}
	}
	rendered, err := RenderKitty(testPNG(t, img))
	if err != nil {
		t.Fatalf("render kitty: %v", err)
	}
	if !strings.HasPrefix(rendered, "\x1b_Ga=T,f=100,q=2,m=1;") || !strings.HasSuffix(rendered, "\x1b\\") {
		t.Fatalf("unexpected kitty framing: %q", rendered[:min(64, len(rendered))])
	}
	if count := strings.Count(rendered, "\x1b_G"); count < 2 {
		t.Fatalf("expected multiple chunks, got %d", count)
	}
	if !strings.Contains(rendered, "m=0;") {
		t.Fatal("final kitty chunk must set m=0")
	}
	if _, err := RenderKitty(nil); err == nil {
		t.Fatal("expected empty image to fail")
	}
}

func TestRenderSixelSnapshot(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{A: 255})
	img.Set(0, 1, color.RGBA{A: 255})
	img.Set(1, 1, color.RGBA{A: 255})
	rendered, err := RenderSixel(testPNG(t, img))
	if err != nil {
		t.Fatalf("render sixel: %v", err)
	}
	want := "\x1bPq\"1;1;2;2#0;2;0;0;0#180;2;100;0;0#0AB$#180@?$-\x1b\\"
	if rendered != want {
		t.Fatalf("sixel = %q, want %q", rendered, want)
	}
}

func TestRenderUnicodeSnapshot(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(0, 1, color.RGBA{A: 255})
	rendered, err := RenderUnicode(testPNG(t, img))
	if err != nil {
		t.Fatalf("render unicode: %v", err)
	}
	want := "\x1b[38;2;255;0;0m\x1b[48;2;0;0;0m▀\x1b[0m\n"
	if rendered != want {
		t.Fatalf("unicode = %q, want %q", rendered, want)
	}
}

func TestRenderFileCreatesPrivateImage(t *testing.T) {
	data := testPNG(t, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	path, err := RenderFile(data)
	if err != nil {
		t.Fatalf("render file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat=%v err=%v", info, err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored bytes mismatch: %d err=%v", len(stored), err)
	}
}
