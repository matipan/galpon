package sessionfile

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errMediaUnavailable = errors.New("attachment unavailable")

func unavailableHistoryMedia(reason string) HistoryPart {
	return HistoryPart{Type: "text", Text: "[Historical attachment is unavailable: " + reason + ". Its original record is retained in the fork's source snapshot.]"}
}

func (h *History) media(block map[string]any) (HistoryPart, error) {
	part := HistoryPart{Type: "image", MIME: str(block["mimeType"]), Data: str(block["data"]), Name: str(block["filename"])}
	if part.Name == "" {
		part.Name = str(block["name"])
	}
	if part.MIME == "" {
		part.MIME = str(block["mime_type"])
	}
	source := object(block["source"])
	if source != nil {
		part.MIME, part.Data = str(source["media_type"]), str(source["data"])
		if source["type"] == "text" {
			part.Data = base64.StdEncoding.EncodeToString([]byte(part.Data))
		}
	}
	if audio := object(block["input_audio"]); audio != nil {
		part.MIME, part.Data = "audio/"+str(audio["format"]), str(audio["data"])
	}
	location := str(block["image_url"])
	if location == "" {
		location = str(object(block["image_url"])["url"])
	}
	for _, value := range []any{block["url"], source["url"], block["audio_url"], block["file_url"], block["path"], source["path"]} {
		if location == "" {
			location = str(value)
		}
	}
	if data := str(block["file_data"]); data != "" {
		if strings.HasPrefix(data, "data:") {
			location = data
		} else {
			part.Data = data
		}
	}
	if strings.HasPrefix(location, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(location, "data:"), ",")
		if !ok {
			return unavailableHistoryMedia("invalid data URL"), nil
		}
		part.MIME = strings.SplitN(header, ";", 2)[0]
		if strings.Contains(header, ";base64") {
			part.Data = data
		} else {
			decoded, err := url.PathUnescape(data)
			if err != nil {
				return unavailableHistoryMedia("invalid data URL"), nil
			}
			part.Data = base64.StdEncoding.EncodeToString([]byte(decoded))
		}
	} else if location != "" {
		cached, err := h.snapshotMedia(location)
		if err != nil {
			if errors.Is(err, errMediaUnavailable) {
				return unavailableHistoryMedia(err.Error()), nil
			}
			return part, err
		}
		data, err := os.ReadFile(cached.Path)
		if err != nil {
			return part, err
		}
		part.Data = base64.StdEncoding.EncodeToString(data)
		if part.MIME == "" {
			part.MIME = cached.MIME
		}
		if part.Name == "" {
			part.Name = cached.Name
		}
	}
	if part.Data == "" {
		if id := str(block["file_id"]); id != "" {
			return unavailableHistoryMedia("provider file " + id + " has no local copy"), nil
		}
		return unavailableHistoryMedia("no saved data or readable location"), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil {
		return unavailableHistoryMedia("invalid base64 data"), nil
	}
	if part.MIME == "" || part.MIME == "application/octet-stream" {
		part.MIME = http.DetectContentType(decoded)
	}
	if str(block["type"]) == "document" || str(block["type"]) == "input_file" || str(block["type"]) == "input_audio" || !strings.HasPrefix(part.MIME, "image/") {
		part.Type = "file"
	}
	if part.Name == "" {
		part.Name = "attachment"
	}
	return part, nil
}

// Cache external bytes once per conversion. A second context projection reads
// the same attachment, even if its original path or URL changes afterwards.
func (h *History) snapshotMedia(location string) (HistoryPart, error) {
	if cached, ok := h.mediaFiles[location]; ok {
		return cached, nil
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return HistoryPart{}, fmt.Errorf("%w: invalid location", errMediaUnavailable)
	}
	var input io.ReadCloser
	mediaType := ""
	name := filepath.Base(parsed.Path)
	switch parsed.Scheme {
	case "http", "https":
		client := &http.Client{Timeout: 30 * time.Second}
		response, err := client.Get(location)
		if err != nil {
			return HistoryPart{}, fmt.Errorf("%w: URL could not be read", errMediaUnavailable)
		}
		input = response.Body
		if response.StatusCode != http.StatusOK {
			_ = input.Close()
			return HistoryPart{}, fmt.Errorf("%w: URL returned HTTP %d", errMediaUnavailable, response.StatusCode)
		}
		mediaType, _, _ = mime.ParseMediaType(response.Header.Get("Content-Type"))
	case "", "file":
		path := location
		if parsed.Scheme == "file" {
			path = parsed.Path
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(h.cwd, path)
		}
		input, err = os.Open(path)
		if err != nil {
			return HistoryPart{}, fmt.Errorf("%w: local file %q could not be read", errMediaUnavailable, path)
		}
		mediaType = mime.TypeByExtension(filepath.Ext(path))
	default:
		return HistoryPart{}, fmt.Errorf("%w: unsupported location scheme %q", errMediaUnavailable, parsed.Scheme)
	}
	defer func() { _ = input.Close() }()
	file, err := os.CreateTemp(h.root, "media-*")
	if err != nil {
		return HistoryPart{}, err
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(file, input); err != nil {
		return HistoryPart{}, err
	}
	cached := HistoryPart{Path: file.Name(), MIME: mediaType, Name: name}
	h.mediaFiles[location] = cached
	return cached, nil
}
