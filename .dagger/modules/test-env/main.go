package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"dagger/test-env/internal/dagger"
)

const (
	goImage       = "golang:1.26.5-trixie"
	nodeImage     = "node:24.19.0-trixie"
	piVersion     = "1.0.4"
	claudeVersion = "2.1.316"
	codexVersion  = "0.155.1"
)

type TestEnv struct {
	// The secret that the ci environment of the workspace gives the module.
	// +private
	CiSecret *dagger.Secret
}

func New(
	// The secret that the ci environment of the workspace gives the module.
	// +optional
	ciSecret *dagger.Secret,
) *TestEnv {
	return &TestEnv{CiSecret: ciSecret}
}

// ciSecretSHA256 is the SHA-256 of the value of the AWS Secrets Manager
// secret dagger-byoc-poc/check-secret of the BYOC test fleet.
const ciSecretSHA256 = "14447e33307f2c2dfe9c7b97c97b35ce295611abd43bfecc708746b0fcb9d42b"

// ReadsTheCiSecret checks that the session of the check read the secret of
// the ci environment from its secret provider in the cloud account of the
// fleet.
// +check
func (m *TestEnv) ReadsTheCiSecret(ctx context.Context) error {
	if m.CiSecret == nil {
		return errors.New("the ci environment of the workspace gives no secret")
	}
	value, err := m.CiSecret.Plaintext(ctx)
	if err != nil {
		return fmt.Errorf("read the secret: %w", err)
	}
	sum := sha256.Sum256([]byte(value))
	if hex.EncodeToString(sum[:]) != ciSecretSHA256 {
		return errors.New("the secret does not have the value of the test fleet")
	}
	return nil
}

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
	neovimURL, neovimChecksum, err := neovimRelease(platform)
	if err != nil {
		return nil, err
	}
	neovim := dag.HTTP(neovimURL, dagger.HTTPOpts{Checksum: neovimChecksum})
	goToolchain := dag.Container().From(goImage).Directory("/usr/local/go")

	return dag.Container().
		From(nodeImage).
		WithDirectory("/usr/local/go", goToolchain).
		WithEnvVariable("PATH", "/opt/galpon-neovim/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin").
		WithEnvVariable("GOBIN", "/usr/local/bin").
		WithEnvVariable("GOFLAGS", "-buildvcs=false").
		WithEnvVariable("PI_CODING_AGENT_DIR", "/tmp/galpon-test-pi").
		WithEnvVariable("GALPON_REQUIRE_NVIM_TESTS", "1").
		WithEnvVariable("GALPON_REQUIRE_NATIVE_TESTS", "1").
		WithFile("/usr/local/bin/herdr", herdr, dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithFile("/tmp/neovim.tar.gz", neovim).
		WithExec([]string{"mkdir", "-p", "/opt/galpon-neovim"}).
		WithExec([]string{"tar", "-xzf", "/tmp/neovim.tar.gz", "--strip-components=1", "-C", "/opt/galpon-neovim"}).
		WithExec([]string{
			"npm", "install", "--global", "--ignore-scripts",
			"@earendil-works/pi-coding-agent@" + piVersion,
			"@anthropic-ai/claude-code@" + claudeVersion,
			"@openai/codex@" + codexVersion,
		}).
		WithExec([]string{"node", "/usr/local/lib/node_modules/@anthropic-ai/claude-code/install.cjs"}).
		WithExec([]string{"claude", "--version"}).
		WithExec([]string{"codex", "--version"}), nil
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
