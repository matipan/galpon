package nativeagent

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A converted session already has a fresh identity. Copy its full saved history
// before native resume so native compaction does not remove archived records.
func stageConvertedSession(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if existing, err := os.Open(target); err == nil {
		defer func() { _ = existing.Close() }()
		left, right := sha256.New(), sha256.New()
		if _, err := io.Copy(left, input); err != nil {
			return err
		}
		if _, err := io.CopyN(right, existing, info.Size()); err != nil {
			return err
		}
		if string(left.Sum(nil)) != string(right.Sum(nil)) {
			return fmt.Errorf("converted conversation and native saved history have different prefixes")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".converted-*")
	if err != nil {
		return err
	}
	defer func() { _ = output.Close(); _ = os.Remove(output.Name()) }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(output.Name(), target); err != nil {
		return err
	}
	folder, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer func() { _ = folder.Close() }()
	return folder.Sync()
}
