package app

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

func imageAttachmentPointer(images []model.ImageAttachment) *[]model.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	return &images
}

func messageImageAttachments(images *[]model.ImageAttachment) []model.ImageAttachment {
	if images == nil {
		return nil
	}
	return *images
}

func imageMetadata(images []model.ImageAttachment) []model.ImageAttachment {
	out := append([]model.ImageAttachment(nil), images...)
	for index := range out {
		out[index].Data = ""
	}
	return out
}

const (
	companionImageCountLimit = 4
	companionImageSizeLimit  = 8 << 20
	companionImageTotalLimit = 20 << 20
)

func validateImageAttachments(images []model.ImageAttachment) ([]model.ImageAttachment, error) {
	if len(images) > companionImageCountLimit {
		return nil, fmt.Errorf("at most four images can be attached")
	}
	out := make([]model.ImageAttachment, len(images))
	total := 0
	for index, input := range images {
		data, err := base64.StdEncoding.DecodeString(input.Data)
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("image %d has invalid base64 data", index+1)
		}
		if len(data) > companionImageSizeLimit {
			return nil, fmt.Errorf("each image must be 8 MiB or smaller")
		}
		total += len(data)
		if total > companionImageTotalLimit {
			return nil, fmt.Errorf("attached images must total 20 MiB or less")
		}
		mimeType := http.DetectContentType(data)
		if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/gif" && mimeType != "image/webp" {
			return nil, fmt.Errorf("image %d must be PNG, JPEG, GIF, or WebP", index+1)
		}
		if input.MimeType != "" && input.MimeType != mimeType {
			return nil, fmt.Errorf("image %d media type does not match its data", index+1)
		}
		width, height, err := imageDimensions(data, mimeType)
		if err != nil || width <= 0 || height <= 0 || width > 65535 || height > 65535 || int64(width)*int64(height) > 100_000_000 {
			return nil, fmt.Errorf("image %d has invalid dimensions", index+1)
		}
		name := cleanImageName(input.Name)
		id := strings.TrimSpace(input.ID)
		if id == "" {
			id = uuid.NewString()
		}
		if len(id) > 200 {
			return nil, fmt.Errorf("image %d has an invalid ID", index+1)
		}
		out[index] = model.ImageAttachment{ID: id, Name: name, MimeType: mimeType, Size: int64(len(data)), Width: width, Height: height, Data: base64.StdEncoding.EncodeToString(data)}
	}
	return out, nil
}

func cleanImageName(value string) string {
	value = filepath.Base(strings.ToValidUTF8(strings.TrimSpace(value), "�"))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if len(value) > 255 {
		value = strings.ToValidUTF8(value[:255], "�")
	}
	return value
}

func imageDimensions(data []byte, mimeType string) (int, int, error) {
	if mimeType == "image/webp" {
		return webPDimensions(data)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	return config.Width, config.Height, err
}

func webPDimensions(data []byte) (int, int, error) {
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || int(binary.LittleEndian.Uint32(data[4:8]))+8 > len(data) {
		return 0, 0, fmt.Errorf("invalid WebP container")
	}
	for offset := 12; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		end := start + size
		if size < 0 || end > len(data) {
			return 0, 0, fmt.Errorf("invalid WebP chunk")
		}
		payload := data[start:end]
		switch kind {
		case "VP8X":
			if len(payload) >= 10 {
				return 1 + int(payload[4]) + int(payload[5])<<8 + int(payload[6])<<16, 1 + int(payload[7]) + int(payload[8])<<8 + int(payload[9])<<16, nil
			}
		case "VP8L":
			if len(payload) >= 5 && payload[0] == 0x2f {
				width := 1 + int(payload[1]) + int(payload[2]&0x3f)<<8
				height := 1 + int(payload[2]>>6) + int(payload[3])<<2 + int(payload[4]&0x0f)<<10
				return width, height, nil
			}
		case "VP8 ":
			if len(payload) >= 10 && payload[3] == 0x9d && payload[4] == 0x01 && payload[5] == 0x2a {
				width := int(binary.LittleEndian.Uint16(payload[6:8]) & 0x3fff)
				height := int(binary.LittleEndian.Uint16(payload[8:10]) & 0x3fff)
				return width, height, nil
			}
		}
		offset = end + size%2
	}
	return 0, 0, fmt.Errorf("WebP dimensions are missing")
}
