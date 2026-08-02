package readimage

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"
)

func prepareFileForAttachment(path, mimeType string, data []byte, raw bool) ([]byte, string, error) {
	if mimeType == "application/pdf" || strings.EqualFold(filepath.Ext(path), ".pdf") {
		return data, "application/pdf", nil
	}

	if !strings.HasPrefix(mimeType, "image/") {
		return data, mimeType, nil
	}
	if _, ok := supportedImageMIMEs[mimeType]; !ok {
		return data, mimeType, fmt.Errorf("cannot attach image at %q: unsupported image format %q", path, mimeType)
	}
	img, decodedMIME, err := decodeSupportedRasterImage(path, data)
	if err != nil {
		return data, mimeType, err
	}
	if raw || int64(len(data)) < minOptimizationSizeBytes {
		return data, decodedMIME, nil
	}

	optimized, optimizedMIME, ok := optimizeRasterImage(img)
	if !ok || len(optimized) >= len(data) {
		return data, decodedMIME, nil
	}
	return optimized, optimizedMIME, nil
}

func decodeSupportedRasterImage(path string, data []byte) (image.Image, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("cannot attach image at %q: unable to decode image: %v", path, err)
	}
	mimeType, ok := mimeTypeForImageFormat(format)
	if !ok {
		return nil, "", fmt.Errorf("cannot attach image at %q: unsupported image format %q", path, format)
	}
	if _, ok := supportedImageMIMEs[mimeType]; !ok {
		return nil, "", fmt.Errorf("cannot attach image at %q: unsupported image format %q", path, mimeType)
	}
	if err := validateDecodedDimensions(path, cfg.Width, cfg.Height); err != nil {
		return nil, "", err
	}
	switch mimeType {
	case "image/gif":
		img, err := decodeStillGIF(path, data)
		if err != nil {
			return nil, "", err
		}
		return img, mimeType, nil
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("cannot attach image at %q: unable to decode image: %v", path, err)
	}
	decodedMIME, ok := mimeTypeForImageFormat(decodedFormat)
	if !ok {
		return nil, "", fmt.Errorf("cannot attach image at %q: unsupported image format %q", path, decodedFormat)
	}
	return img, decodedMIME, nil
}

func decodeStillGIF(path string, data []byte) (image.Image, error) {
	frames, err := countGIFFrames(data, 2)
	if err != nil {
		return nil, fmt.Errorf("cannot attach GIF at %q: %v", path, err)
	}
	if frames != 1 {
		return nil, fmt.Errorf("cannot attach GIF at %q: animated GIFs are not supported; use a still image or PDF", path)
	}
	img, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot attach GIF at %q: %v", path, err)
	}
	return img, nil
}

func validateDecodedDimensions(path string, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("cannot attach image at %q: invalid image dimensions %dx%d", path, width, height)
	}
	pixels := int64(width) * int64(height)
	if pixels > maxDecodedPixels {
		return fmt.Errorf("cannot attach image at %q: decoded image dimensions %dx%d exceed the supported pixel limit of %d", path, width, height, maxDecodedPixels)
	}
	return nil
}

func mimeTypeForImageFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return "image/png", true
	case "jpeg":
		return "image/jpeg", true
	case "gif":
		return "image/gif", true
	default:
		return "", false
	}
}

func optimizeRasterImage(img image.Image) ([]byte, string, bool) {
	if img == nil {
		return nil, "", false
	}
	bounds := img.Bounds()
	if bounds.Empty() {
		return nil, "", false
	}
	opaque := image.NewRGBA(bounds)
	draw.Draw(opaque, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(opaque, bounds, img, bounds.Min, draw.Over)
	for _, quality := range []int{85, 75, 65, 55} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, opaque, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", false
		}
		if int64(out.Len()) <= maxFileSizeBytes {
			return out.Bytes(), "image/jpeg", true
		}
	}
	return nil, "", false
}
