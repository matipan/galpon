package main

import (
	"fmt"

	"dagger/test-env/internal/dagger"
)

// Test the supported 0.11 release, not the developer's editor configuration.
func neovimRelease(platform dagger.Platform) (string, string, error) {
	const release = "https://github.com/neovim/neovim/releases/download/v0.11.5/"
	switch platform {
	case "linux/amd64":
		return release + "nvim-linux-x86_64.tar.gz", "sha256:b2f91117be5b5ea39edd7297156dc2a4a8df4add6c95a90809a8df19e7ab6f52", nil
	case "linux/arm64":
		return release + "nvim-linux-arm64.tar.gz", "sha256:ea4f9a31b11cc1477ff014aebb7b207684e7280f94ffa97abdab6cacd9b98519", nil
	default:
		return "", "", fmt.Errorf("unsupported Neovim test platform %q", platform)
	}
}
