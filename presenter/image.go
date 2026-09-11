// Package presenter renders short-lived auth challenge images for terminal
// clients. It never owns a browser page or a login flow.
package presenter

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"slices"
	"strings"
)

type Protocol string

const (
	ProtocolAuto    Protocol = "auto"
	ProtocolKitty   Protocol = "kitty"
	ProtocolSixel   Protocol = "sixel"
	ProtocolUnicode Protocol = "unicode"
	ProtocolFile    Protocol = "file"
)

func ParseProtocol(value string) (Protocol, error) {
	switch Protocol(strings.ToLower(strings.TrimSpace(value))) {
	case "", ProtocolAuto:
		return ProtocolAuto, nil
	case ProtocolKitty:
		return ProtocolKitty, nil
	case ProtocolSixel:
		return ProtocolSixel, nil
	case ProtocolUnicode:
		return ProtocolUnicode, nil
	case ProtocolFile:
		return ProtocolFile, nil
	default:
		return "", fmt.Errorf("unknown image protocol %q", value)
	}
}

// DetectProtocol resolves auto to the best supported renderer. Kitty
// advertises KITTY_WINDOW_ID; Sixel is detected only from explicit terminal
// signals; everything else falls back to the Unicode preview.
func DetectProtocol(requested Protocol, environment map[string]string) Protocol {
	switch requested {
	case ProtocolKitty, ProtocolSixel, ProtocolUnicode, ProtocolFile:
		return requested
	}
	if strings.TrimSpace(environment["KITTY_WINDOW_ID"]) != "" {
		return ProtocolKitty
	}
	term := strings.ToLower(environment["TERM"])
	termProgram := strings.ToLower(environment["TERM_PROGRAM"])
	if strings.Contains(term, "sixel") || termProgram == "sixel" {
		return ProtocolSixel
	}
	return ProtocolUnicode
}

const kittyChunkSize = 4096

// RenderKitty returns chunked Kitty graphics protocol commands for one PNG.
func RenderKitty(pngData []byte) (string, error) {
	if len(pngData) == 0 {
		return "", errors.New("kitty renderer requires image data")
	}
	encoded := base64.StdEncoding.EncodeToString(pngData)
	var builder strings.Builder
	for offset := 0; offset < len(encoded); offset += kittyChunkSize {
		end := offset + kittyChunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		more := 1
		if end == len(encoded) {
			more = 0
		}
		fmt.Fprintf(&builder, "\x1b_Ga=T,f=100,q=2,m=%d;%s\x1b\\", more, encoded[offset:end])
	}
	return builder.String(), nil
}

// RenderSixel returns a bounded Sixel raster for one PNG. Colors are quantized
// to the 6x6x6 web palette; pixels below half alpha render black.
func RenderSixel(pngData []byte) (string, error) {
	img, err := decodeBounded(pngData, maximumSixelWidth, maximumSixelHeight)
	if err != nil {
		return "", err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return "", errors.New("sixel renderer received an empty image")
	}
	var builder strings.Builder
	builder.WriteString("\x1bPq")
	fmt.Fprintf(&builder, "\"1;1;%d;%d", width, height)

	used := make(map[int]struct{})
	pixels := make([]int, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			red, green, blue, alpha := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			index := 0
			if alpha >= 0x8000 {
				index = webPaletteIndex(uint8(red>>8), uint8(green>>8), uint8(blue>>8))
			}
			pixels[y*width+x] = index
			used[index] = struct{}{}
		}
	}
	for index := 0; index < 216; index++ {
		if _, exists := used[index]; !exists {
			continue
		}
		red, green, blue := webPaletteColor(index)
		fmt.Fprintf(&builder, "#%d;2;%d;%d;%d", index, int(red)*100/255, int(green)*100/255, int(blue)*100/255)
	}
	for top := 0; top < height; top += 6 {
		colors := make([]int, 0, len(used))
		for index := range used {
			colors = append(colors, index)
		}
		slices.Sort(colors)
		for _, index := range colors {
			// Emit the column run for this color in one pass so absent columns
			// stay blank.
			fmt.Fprintf(&builder, "#%d", index)
			for x := 0; x < width; x++ {
				bits := 0
				for row := 0; row < 6; row++ {
					y := top + row
					if y < height && pixels[y*width+x] == index {
						bits |= 1 << row
					}
				}
				builder.WriteByte(byte(63 + bits))
			}
			builder.WriteByte('$')
		}
		builder.WriteByte('-')
	}
	builder.WriteString("\x1b\\")
	return builder.String(), nil
}

const (
	maximumSixelWidth  = 320
	maximumSixelHeight = 480
	maximumUnicodeCols = 80
	maximumUnicodeRows = 40
)

func decodeBounded(pngData []byte, maxWidth, maxHeight int) (image.Image, error) {
	decoded, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("decode challenge image: %w", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() <= maxWidth && bounds.Dy() <= maxHeight {
		return decoded, nil
	}
	scale := 1.0
	if bounds.Dx() > maxWidth {
		scale = float64(maxWidth) / float64(bounds.Dx())
	}
	if height := float64(bounds.Dy()) * scale; height > float64(maxHeight) {
		scale = float64(maxHeight) / float64(bounds.Dy())
	}
	targetWidth := int(float64(bounds.Dx()) * scale)
	targetHeight := int(float64(bounds.Dy()) * scale)
	if targetWidth < 1 {
		targetWidth = 1
	}
	if targetHeight < 1 {
		targetHeight = 1
	}
	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/targetHeight
		for x := 0; x < targetWidth; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/targetWidth
			target.Set(x, y, decoded.At(sourceX, sourceY))
		}
	}
	return target, nil
}

func webPaletteIndex(red, green, blue uint8) int {
	return int(red/51)*36 + int(green/51)*6 + int(blue/51)
}

func webPaletteColor(index int) (uint8, uint8, uint8) {
	red := uint8((index / 36) * 51)
	green := uint8(((index / 6) % 6) * 51)
	blue := uint8((index % 6) * 51)
	return red, green, blue
}

// RenderUnicode returns an upper-half-block preview with 24-bit colors.
func RenderUnicode(pngData []byte) (string, error) {
	img, err := decodeBounded(pngData, maximumUnicodeCols, maximumUnicodeRows*2)
	if err != nil {
		return "", err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return "", errors.New("unicode renderer received an empty image")
	}
	var builder strings.Builder
	for y := 0; y < height; y += 2 {
		for x := 0; x < width; x++ {
			topRed, topGreen, topBlue, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			bottomRed, bottomGreen, bottomBlue := topRed, topGreen, topBlue
			if y+1 < height {
				bottomRed, bottomGreen, bottomBlue, _ = img.At(bounds.Min.X+x, bounds.Min.Y+y+1).RGBA()
			}
			fmt.Fprintf(&builder, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
				topRed>>8, topGreen>>8, topBlue>>8, bottomRed>>8, bottomGreen>>8, bottomBlue>>8)
		}
		builder.WriteString("\x1b[0m\n")
	}
	return builder.String(), nil
}

// RenderFile stores the image in a private temporary file and returns its
// path. The caller owns cleanup.
func RenderFile(pngData []byte) (string, error) {
	if len(pngData) == 0 {
		return "", errors.New("file renderer requires image data")
	}
	file, err := os.CreateTemp("", "job-agent-challenge-*.png")
	if err != nil {
		return "", fmt.Errorf("create challenge file: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("protect challenge file: %w", err)
	}
	if _, err := file.Write(pngData); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write challenge file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close challenge file: %w", err)
	}
	return path, nil
}
