package checkpoint

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/gitx"
	"github.com/matipan/galpon/internal/model"
)

const (
	FormatVersion        = 2
	legacyFormatVersion  = 1
	chunkSize            = 1 << 20
	manifestSizeLimit    = 32 << 20
	checkpointImageLimit = 8 << 20
	kdfIterations        = 600_000
)

var magic = []byte("GALPON-CHECKPOINT\n")

type Manifest struct {
	FormatVersion  int                       `json:"formatVersion"`
	ID             string                    `json:"id"`
	CreatedAt      time.Time                 `json:"createdAt"`
	SourceStateDir string                    `json:"sourceStateDir"`
	State          model.DurableState        `json:"state"`
	Git            []gitx.CheckpointSnapshot `json:"git"`
}

type checkpointImage struct {
	id   string
	data []byte
}

func manifestWithoutImageData(manifest Manifest) (Manifest, []checkpointImage, error) {
	if manifest.FormatVersion != FormatVersion {
		return Manifest{}, nil, fmt.Errorf("checkpoint format %d cannot be written", manifest.FormatVersion)
	}
	manifest.State.Messages = append([]model.AgentMessage(nil), manifest.State.Messages...)
	images := make([]checkpointImage, 0)
	seen := make(map[string]bool)
	for messageIndex := range manifest.State.Messages {
		message := &manifest.State.Messages[messageIndex]
		if message.Images == nil {
			continue
		}
		metadata := append([]model.ImageAttachment(nil), (*message.Images)...)
		for imageIndex := range metadata {
			image := &metadata[imageIndex]
			if image.ID == "" || image.ID == "." || path.Base(image.ID) != image.ID || strings.Contains(image.ID, `\`) || seen[image.ID] {
				return Manifest{}, nil, fmt.Errorf("checkpoint message %s has an invalid image ID", message.ID)
			}
			data, err := base64.StdEncoding.DecodeString(image.Data)
			if err != nil || image.Size <= 0 || image.Size > checkpointImageLimit || int64(len(data)) != image.Size {
				return Manifest{}, nil, fmt.Errorf("checkpoint message %s has invalid image data", message.ID)
			}
			seen[image.ID] = true
			images = append(images, checkpointImage{id: image.ID, data: data})
			image.Data = ""
		}
		message.Images = &metadata
	}
	return manifest, images, nil
}

func manifestImageTargets(manifest *Manifest) (map[string]*model.ImageAttachment, error) {
	targets := make(map[string]*model.ImageAttachment)
	for messageIndex := range manifest.State.Messages {
		message := &manifest.State.Messages[messageIndex]
		if message.Images == nil {
			continue
		}
		for imageIndex := range *message.Images {
			image := &(*message.Images)[imageIndex]
			if image.ID == "" || image.ID == "." || path.Base(image.ID) != image.ID || strings.Contains(image.ID, `\`) || targets[image.ID] != nil || image.Data != "" || image.Size <= 0 || image.Size > checkpointImageLimit {
				return nil, fmt.Errorf("checkpoint message %s has invalid image metadata", message.ID)
			}
			targets[image.ID] = image
		}
	}
	return targets, nil
}

type encryptionHeader struct {
	Version    int    `json:"version"`
	Cipher     string `json:"cipher"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	ChunkSize  int    `json:"chunkSize"`
}

// Write creates an encrypted checkpoint atomically. It copies session files
// and managed directories only for agents that are present in the manifest.
func Write(ctx context.Context, filePath, passphrase, stateDir string, manifest Manifest) error {
	if strings.TrimSpace(passphrase) == "" {
		return fmt.Errorf("checkpoint passphrase is required")
	}
	portableManifest, images, err := manifestWithoutImageData(manifest)
	if err != nil {
		return err
	}
	manifestData, err := json.Marshal(portableManifest)
	if err != nil {
		return err
	}
	if len(manifestData) > manifestSizeLimit {
		return fmt.Errorf("checkpoint manifest exceeds the %d MiB limit", manifestSizeLimit>>20)
	}
	filePath, err = filepath.Abs(strings.TrimSpace(filePath))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("checkpoint file already exists: %s", filePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".galpon-checkpoint-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	complete := false
	defer func() {
		_ = temporary.Close()
		if !complete {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encrypted, err := newEncryptWriter(temporary, passphrase)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	if err := writeTarBytes(archive, "manifest.json", manifestData); err != nil {
		return err
	}
	for _, image := range images {
		if err := writeTarBytes(archive, path.Join("images", image.id), image.data); err != nil {
			return err
		}
	}
	for _, agent := range manifest.State.Agents {
		if err := ctx.Err(); err != nil {
			return err
		}
		sessions := filepath.Join(stateDir, "agents", agent.ID, "sessions")
		if info, err := os.Stat(sessions); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("agent %s session path is not a directory", agent.ID)
			}
			if err := addDirectory(ctx, archive, sessions, path.Join("agents", agent.ID, "sessions")); err != nil {
				return fmt.Errorf("archive sessions for agent %s: %w", agent.ID, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read sessions for agent %s: %w", agent.ID, err)
		}
		if directory, archivePath, ok := managedAgentDirectory(stateDir, agent); ok {
			info, err := os.Stat(directory)
			if err != nil {
				return fmt.Errorf("read managed directory for agent %s: %w", agent.ID, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("managed path for agent %s is not a directory", agent.ID)
			}
			if err := addDirectory(ctx, archive, directory, archivePath); err != nil {
				return fmt.Errorf("archive managed directory for agent %s: %w", agent.ID, err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("checkpoint file already exists: %s", filePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return err
	}
	complete = true
	return nil
}

// Read decrypts a checkpoint and extracts its Pi sessions into destination.
func Read(ctx context.Context, filePath, passphrase, destination string) (Manifest, error) {
	var manifest Manifest
	if strings.TrimSpace(passphrase) == "" {
		return manifest, fmt.Errorf("checkpoint passphrase is required")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close() }()
	decrypted, err := newDecryptReader(file, passphrase)
	if err != nil {
		return manifest, err
	}
	compressed, err := gzip.NewReader(decrypted)
	if err != nil {
		return manifest, fmt.Errorf("decrypt checkpoint: %w", err)
	}
	archive := tar.NewReader(compressed)
	seenManifest := false
	imageTargets := map[string]*model.ImageAttachment{}
	seenImages := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, fmt.Errorf("read checkpoint archive: %w", err)
		}
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return manifest, err
		}
		if name == "manifest.json" {
			if seenManifest || header.Typeflag != tar.TypeReg || header.Size > manifestSizeLimit {
				return manifest, fmt.Errorf("invalid checkpoint manifest")
			}
			if err := json.NewDecoder(io.LimitReader(archive, header.Size)).Decode(&manifest); err != nil {
				return manifest, fmt.Errorf("decode checkpoint manifest: %w", err)
			}
			seenManifest = true
			if manifest.FormatVersion != legacyFormatVersion && manifest.FormatVersion != FormatVersion {
				return manifest, fmt.Errorf("checkpoint format %d is not supported", manifest.FormatVersion)
			}
			if manifest.FormatVersion == FormatVersion {
				imageTargets, err = manifestImageTargets(&manifest)
				if err != nil {
					return manifest, err
				}
			}
			continue
		}
		if !seenManifest {
			return manifest, fmt.Errorf("checkpoint manifest must be the first archive entry")
		}
		if strings.HasPrefix(name, "images/") {
			imageID := strings.TrimPrefix(name, "images/")
			image := imageTargets[imageID]
			if manifest.FormatVersion != FormatVersion || image == nil || seenImages[imageID] || header.Typeflag != tar.TypeReg || header.Size != image.Size || header.Size > checkpointImageLimit {
				return manifest, fmt.Errorf("invalid checkpoint image entry %q", name)
			}
			data := make([]byte, header.Size)
			if _, err := io.ReadFull(archive, data); err != nil {
				return manifest, fmt.Errorf("read checkpoint image %s: %w", imageID, err)
			}
			image.Data = base64.StdEncoding.EncodeToString(data)
			seenImages[imageID] = true
			continue
		}
		if !strings.HasPrefix(name, "agents/") {
			return manifest, fmt.Errorf("unexpected checkpoint archive entry %q", name)
		}
		target, err := archiveTarget(destination, name)
		if err != nil {
			return manifest, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return manifest, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return manifest, err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return manifest, err
			}
			_, copyErr := io.CopyN(output, archive, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return manifest, copyErr
			}
			if closeErr != nil {
				return manifest, closeErr
			}
		default:
			return manifest, fmt.Errorf("checkpoint archive entry %q has unsupported type", name)
		}
	}
	if err := compressed.Close(); err != nil {
		return manifest, err
	}
	if _, err := io.Copy(io.Discard, decrypted); err != nil {
		return manifest, fmt.Errorf("authenticate checkpoint: %w", err)
	}
	if !seenManifest {
		return manifest, fmt.Errorf("checkpoint manifest is missing")
	}
	if len(seenImages) != len(imageTargets) {
		return manifest, fmt.Errorf("checkpoint image data is incomplete")
	}
	return manifest, nil
}

func writeTarBytes(archive *tar.Writer, name string, data []byte) error {
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := archive.Write(data)
	return err
}

func addDirectory(ctx context.Context, archive *tar.Writer, root, archiveRoot string) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		name := archiveRoot
		if relative != "." {
			name = path.Join(archiveRoot, filepath.ToSlash(relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archived directory contains a symbolic link: %s", filePath)
		}
		if entry.IsDir() {
			return archive.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o700, Typeflag: tar.TypeDir})
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archived path is not a regular file: %s", filePath)
		}
		header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archive, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func managedAgentDirectory(stateDir string, agent model.Agent) (string, string, bool) {
	if agent.Placement.Type != "none" || strings.TrimSpace(agent.Placement.CWD) == "" {
		return "", "", false
	}
	root := filepath.Join(stateDir, "agents", agent.ID)
	directory := filepath.Clean(agent.Placement.CWD)
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", false
	}
	return directory, path.Join("agents", agent.ID, filepath.ToSlash(relative)), true
}

func safeArchiveName(name string) (string, error) {
	name = path.Clean(strings.TrimSpace(name))
	if name == "." || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("checkpoint archive path is not safe: %q", name)
	}
	return name, nil
}

func archiveTarget(root, name string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(name))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("checkpoint archive path leaves the destination: %q", name)
	}
	return target, nil
}

type encryptWriter struct {
	output  io.Writer
	aead    cipher.AEAD
	header  []byte
	nonce   []byte
	pending []byte
	counter uint32
	closed  bool
	err     error
}

func newEncryptWriter(output io.Writer, passphrase string) (*encryptWriter, error) {
	header := encryptionHeader{
		Version: 1, Cipher: "AES-256-GCM", KDF: "PBKDF2-SHA256",
		Iterations: kdfIterations, Salt: make([]byte, 16), Nonce: make([]byte, 8), ChunkSize: chunkSize,
	}
	if _, err := rand.Read(header.Salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(header.Nonce); err != nil {
		return nil, err
	}
	headerData, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, header.Salt, header.Iterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if _, err := output.Write(magic); err != nil {
		return nil, err
	}
	if err := binary.Write(output, binary.BigEndian, uint32(len(headerData))); err != nil {
		return nil, err
	}
	if _, err := output.Write(headerData); err != nil {
		return nil, err
	}
	return &encryptWriter{output: output, aead: aead, header: headerData, nonce: header.Nonce, pending: make([]byte, 0, chunkSize)}, nil
}

func (w *encryptWriter) Write(data []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("checkpoint encryption stream is closed")
	}
	if w.err != nil {
		return 0, w.err
	}
	written := len(data)
	for len(data) != 0 {
		space := chunkSize - len(w.pending)
		if space > len(data) {
			space = len(data)
		}
		w.pending = append(w.pending, data[:space]...)
		data = data[space:]
		if len(w.pending) == chunkSize {
			w.err = w.writeRecord(0, w.pending)
			w.pending = w.pending[:0]
			if w.err != nil {
				return 0, w.err
			}
		}
	}
	return written, nil
}

func (w *encryptWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil && len(w.pending) != 0 {
		w.err = w.writeRecord(0, w.pending)
	}
	if w.err == nil {
		w.err = w.writeRecord(1, nil)
	}
	return w.err
}

func (w *encryptWriter) writeRecord(flag byte, plain []byte) error {
	if w.counter == ^uint32(0) {
		return fmt.Errorf("checkpoint is too large")
	}
	nonce := recordNonce(w.nonce, w.counter)
	aad := recordAAD(w.header, flag, w.counter)
	sealed := w.aead.Seal(nil, nonce, plain, aad)
	if _, err := w.output.Write([]byte{flag}); err != nil {
		return err
	}
	if err := binary.Write(w.output, binary.BigEndian, uint32(len(sealed))); err != nil {
		return err
	}
	if _, err := w.output.Write(sealed); err != nil {
		return err
	}
	w.counter++
	return nil
}

type decryptReader struct {
	input   io.Reader
	aead    cipher.AEAD
	header  []byte
	nonce   []byte
	chunk   int
	counter uint32
	plain   []byte
	final   bool
	err     error
}

func newDecryptReader(input io.Reader, passphrase string) (*decryptReader, error) {
	prefix := make([]byte, len(magic))
	if _, err := io.ReadFull(input, prefix); err != nil || string(prefix) != string(magic) {
		return nil, fmt.Errorf("not a Galpon checkpoint")
	}
	var headerSize uint32
	if err := binary.Read(input, binary.BigEndian, &headerSize); err != nil || headerSize == 0 || headerSize > 1<<20 {
		return nil, fmt.Errorf("invalid checkpoint encryption header")
	}
	headerData := make([]byte, headerSize)
	if _, err := io.ReadFull(input, headerData); err != nil {
		return nil, err
	}
	var header encryptionHeader
	if err := json.Unmarshal(headerData, &header); err != nil {
		return nil, fmt.Errorf("decode checkpoint encryption header: %w", err)
	}
	if header.Version != 1 || header.Cipher != "AES-256-GCM" || header.KDF != "PBKDF2-SHA256" || header.Iterations < 100_000 || header.Iterations > 5_000_000 || len(header.Salt) != 16 || len(header.Nonce) != 8 || header.ChunkSize < 1 || header.ChunkSize > 16<<20 {
		return nil, fmt.Errorf("unsupported checkpoint encryption settings")
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, header.Salt, header.Iterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &decryptReader{input: input, aead: aead, header: headerData, nonce: header.Nonce, chunk: header.ChunkSize}, nil
}

func (r *decryptReader) Read(output []byte) (int, error) {
	for len(r.plain) == 0 && r.err == nil && !r.final {
		r.readRecord()
	}
	if len(r.plain) != 0 {
		count := copy(output, r.plain)
		r.plain = r.plain[count:]
		return count, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *decryptReader) readRecord() {
	var flag [1]byte
	if _, err := io.ReadFull(r.input, flag[:]); err != nil {
		r.err = fmt.Errorf("checkpoint is incomplete: %w", err)
		return
	}
	if flag[0] != 0 && flag[0] != 1 {
		r.err = fmt.Errorf("checkpoint has an invalid encrypted record")
		return
	}
	var size uint32
	if err := binary.Read(r.input, binary.BigEndian, &size); err != nil {
		r.err = fmt.Errorf("checkpoint is incomplete: %w", err)
		return
	}
	if size < uint32(r.aead.Overhead()) || size > uint32(r.chunk+r.aead.Overhead()) {
		r.err = fmt.Errorf("checkpoint has an invalid encrypted record size")
		return
	}
	sealed := make([]byte, size)
	if _, err := io.ReadFull(r.input, sealed); err != nil {
		r.err = fmt.Errorf("checkpoint is incomplete: %w", err)
		return
	}
	nonce := recordNonce(r.nonce, r.counter)
	aad := recordAAD(r.header, flag[0], r.counter)
	plain, err := r.aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		r.err = fmt.Errorf("checkpoint authentication failed")
		return
	}
	r.counter++
	if flag[0] == 1 {
		if len(plain) != 0 {
			r.err = fmt.Errorf("checkpoint has an invalid final record")
			return
		}
		var trailing [1]byte
		if count, err := r.input.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
			r.err = fmt.Errorf("checkpoint has data after its final record")
			return
		}
		r.final = true
		return
	}
	r.plain = plain
}

func recordNonce(prefix []byte, counter uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], counter)
	return nonce
}

func recordAAD(header []byte, flag byte, counter uint32) []byte {
	aad := make([]byte, len(header)+5)
	copy(aad, header)
	aad[len(header)] = flag
	binary.BigEndian.PutUint32(aad[len(header)+1:], counter)
	return aad
}
