package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const companionAudioLimit = 12 << 20

var (
	errInvalidCompanionAudio = errors.New("invalid companion audio")
	errCompanionAudioEmpty   = errors.New("companion audio contains no speech")
)

type companionAudioTranscriber interface {
	Transcribe(context.Context, io.Reader, string) (string, error)
}

type voxtypeAudioTranscriber struct {
	ffmpeg  string
	voxtype string
}

func newVoxtypeAudioTranscriber() companionAudioTranscriber {
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	voxtype, voxtypeErr := exec.LookPath("voxtype")
	if ffmpegErr != nil || voxtypeErr != nil {
		return nil
	}
	return voxtypeAudioTranscriber{ffmpeg: ffmpeg, voxtype: voxtype}
}

func (v voxtypeAudioTranscriber) Transcribe(ctx context.Context, source io.Reader, language string) (string, error) {
	directory, err := os.MkdirTemp("", "galpon-companion-audio-")
	if err != nil {
		return "", fmt.Errorf("create private audio directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(directory) }()

	sourcePath := filepath.Join(directory, "recording")
	sourceFile, err := os.OpenFile(sourcePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temporary audio file: %w", err)
	}
	written, copyErr := io.Copy(sourceFile, source)
	closeErr := sourceFile.Close()
	if copyErr != nil {
		return "", fmt.Errorf("store temporary audio: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close temporary audio: %w", closeErr)
	}
	if written == 0 {
		return "", errInvalidCompanionAudio
	}

	wavPath := filepath.Join(directory, "recording.wav")
	conversion := exec.CommandContext(ctx, v.ffmpeg,
		"-nostdin", "-loglevel", "error", "-y", "-i", sourcePath,
		"-t", "120", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wavPath,
	)
	if output, err := conversion.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: ffmpeg: %v: %s", errInvalidCompanionAudio, err, strings.TrimSpace(string(output)))
	}

	command := exec.CommandContext(ctx, v.voxtype, "--quiet", "--language", language, "transcribe", wavPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("voxtype: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	transcript := cleanVoxtypeTranscript(stdout.String())
	if transcript == "" {
		return "", errCompanionAudioEmpty
	}
	return transcript, nil
}

func cleanVoxtypeTranscript(value string) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\r\n", "\n")
	lines := strings.Split(value, "\n")
	index := 0
	removedStatus := false
	for index < len(lines) {
		line := strings.TrimSpace(lines[index])
		status := strings.HasPrefix(line, "Loading audio file:") ||
			strings.HasPrefix(line, "Audio format:") ||
			(strings.HasPrefix(line, "Processing ") && strings.Contains(line, " samples ("))
		if status {
			removedStatus = true
			index++
			continue
		}
		if line == "" && removedStatus {
			index++
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[index:], "\n"))
}
