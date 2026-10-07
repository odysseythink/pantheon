package imgateway

// Media storage abstraction.
//
// Defines a pluggable backend for persisting binary media payloads. All file
// I/O performed by channels (inbound persistence, outbound bytes loading)
// goes through this layer, so the actual storage location (local filesystem,
// object store, in-memory cache) is irrelevant to channel implementations.
//
// Platform-specific upload/download logic lives on the channel itself:
//   - inbound:  BaseChannel.FetchRemoteMedia(url)  — platform auth GET
//   - outbound: BaseChannel.LoadMediaBytes(part)   — read backend bytes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ErrMediaNotFound is returned by MediaBackend.Read when a key is missing
// (mirrors Python FileNotFoundError).
var ErrMediaNotFound = errors.New("media not found")

// MediaBackend is the abstract backend for persisting media files.
//
// Backends are addressed by an opaque key (string). The key is also what gets
// stamped onto ContentPart.LocalPath for downstream readers, so its semantics
// are fully owned by the backend implementation.
type MediaBackend interface {
	// Save persists media data under the given key.
	Save(ctx context.Context, data []byte, key string) error
	// Read reads previously saved media data. Returns ErrMediaNotFound if missing.
	Read(ctx context.Context, key string) ([]byte, error)
	// Exists checks whether a key has been saved.
	Exists(ctx context.Context, key string) bool
	// GetLocalPath resolves the on-disk path for a key, when one exists.
	// Returns "" when the backend is not filesystem-backed; callers must
	// fall back to Read for bytes.
	GetLocalPath(key string) string
}

// FileSystemMediaBackend is a filesystem-based media backend. Stores files at
// {rootPath}/{key}.
type FileSystemMediaBackend struct {
	root string
}

// NewFileSystemMediaBackend builds a backend rooted at rootPath.
func NewFileSystemMediaBackend(rootPath string) *FileSystemMediaBackend {
	return &FileSystemMediaBackend{root: rootPath}
}

// RootPath returns the backend root directory.
func (b *FileSystemMediaBackend) RootPath() string { return b.root }

// Save persists data under key, creating parent directories as needed.
func (b *FileSystemMediaBackend) Save(_ context.Context, data []byte, key string) error {
	path := filepath.Join(b.root, filepath.FromSlash(key))
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// Read returns the stored bytes. Returns ErrMediaNotFound when missing.
func (b *FileSystemMediaBackend) Read(_ context.Context, key string) ([]byte, error) {
	path := filepath.Join(b.root, filepath.FromSlash(key))
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMediaNotFound
		}
		return nil, err
	}
	return os.ReadFile(path)
}

// Exists reports whether the key exists on disk.
func (b *FileSystemMediaBackend) Exists(_ context.Context, key string) bool {
	_, err := os.Stat(filepath.Join(b.root, filepath.FromSlash(key)))
	return err == nil
}

// GetLocalPath resolves the on-disk path for a key.
func (b *FileSystemMediaBackend) GetLocalPath(key string) string {
	return filepath.Join(b.root, filepath.FromSlash(key))
}
