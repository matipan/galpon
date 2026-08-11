package main

import (
	"context"
	"fmt"

	"dagger/test-env/internal/dagger"
)

const (
	goImage   = "golang:1.26.5-trixie"
	nodeImage = "node:24.19.0-trixie"
	piVersion = "0.84.1"
)

type TestEnv struct{}

// Base returns the complete container used by the Go module for tests.
func (m *TestEnv) Base(ctx context.Context) (*dagger.Container, error) {
	platform, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return nil, fmt.Errorf("get default platform: %w", err)
	}

	herdrURL, herdrChecksum, err := herdrRelease(platform)
	if err != nil {
		return nil, err
	}
	herdr := dag.HTTP(herdrURL, dagger.HTTPOpts{Checksum: herdrChecksum})
	goToolchain := dag.Container().From(goImage).Directory("/usr/local/go")

	return dag.Container().
		From(nodeImage).
		WithDirectory("/usr/local/go", goToolchain).
		WithEnvVariable("PATH", "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin").
		WithEnvVariable("GOBIN", "/usr/local/bin").
		WithEnvVariable("GOFLAGS", "-buildvcs=false").
		WithFile("/usr/local/bin/herdr", herdr, dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithExec([]string{
			"npm", "install", "--global", "--ignore-scripts",
			"@earendil-works/pi-coding-agent@" + piVersion,
		}), nil
}

func herdrRelease(platform dagger.Platform) (string, string, error) {
	const release = "https://github.com/herdrdev/herdr/releases/download/v0.7.5/"

	switch platform {
	case "linux/amd64":
		return release + "herdr-linux-x86_64", "sha256:3dc83288073e4c2d3c679a30e7be97bcca9141c6fd17dbbb9219142e95c59253", nil
	case "linux/arm64":
		return release + "herdr-linux-aarch64", "sha256:32e763a1499a6b694b1d708e4f062b743be1da9f34fcfa4d212d6db6fe09a8b9", nil
	default:
		return "", "", fmt.Errorf("unsupported test platform %q", platform)
	}
}
