package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const (
	bundleVersion    = 1
	bundleVersionKey = "jackstay_profile_bundle"
	maxBundleBytes   = 8 << 20
	maxTemplateBytes = 1 << 20
)

type bundlePayload struct {
	Format    int               `json:"jackstay_profile_bundle"`
	Profile   json.RawMessage   `json:"profile"`
	Templates map[string][]byte `json:"templates"`
}

type bundle struct {
	bundlePayload
	ContentSHA256 string `json:"content_sha256"`
}

// export bundles exactly the template bytes the ordinary profile loader validated. Paths are
// bundle-local keys. A recipient never needs the producer's folder or loose template files.
func export(path string) ([]byte, error) {
	data, err := readProfileFile(path, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	read := templatesFromFolder(filepath.Dir(path))
	files := make(map[string][]byte)
	total := len(canonical)
	_, err = readProfile(path, parsed, func(name string) ([]byte, error) {
		if stored, exists := files[name]; exists {
			return stored, nil
		}
		content, err := read(name)
		if err != nil {
			return nil, err
		}
		total += len(content) + len(name)
		if total > maxBundleBytes {
			return nil, errors.New("profile bundle is larger than 8 MiB")
		}
		files[name] = content
		return content, nil
	})
	if err != nil {
		return nil, err
	}
	payload := bundlePayload{Format: bundleVersion, Profile: canonical, Templates: files}
	canonicalPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(bundle{bundlePayload: payload, ContentSHA256: sha256Hex(canonicalPayload)})
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 > maxBundleBytes {
		return nil, errors.New("profile bundle is larger than 8 MiB")
	}
	return append(encoded, '\n'), nil
}

func loadBundle(path string, data []byte) (*Profile, error) {
	var bundled bundle
	if err := strictjson.Unmarshal(data, &bundled); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if bundled.Format != bundleVersion {
		return nil, fmt.Errorf("%s: unsupported profile bundle format %d", path, bundled.Format)
	}
	encoded, err := json.Marshal(bundled.bundlePayload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if sha256Hex(encoded) != bundled.ContentSHA256 {
		return nil, fmt.Errorf("%s: the profile bundle's content SHA-256 does not match", path)
	}
	parsed, err := config.Parse(bundled.Profile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	used := make(map[string]bool, len(bundled.Templates))
	profile, err := readProfile(path, parsed, func(name string) ([]byte, error) {
		data, exists := bundled.Templates[name]
		if !exists {
			return nil, fmt.Errorf("template %q is missing from the bundle", name)
		}
		if len(data) > maxTemplateBytes {
			return nil, fmt.Errorf("template %q is larger than 1 MiB", name)
		}
		used[name] = true
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	if len(used) != len(bundled.Templates) {
		return nil, fmt.Errorf("%s: the bundle contains an unreferenced template", path)
	}
	return profile, nil
}

// templatesFromFolder prevents a relative template path from reading outside the profile via a symlink.
func templatesFromFolder(folder string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		root, err := filepath.EvalSymlinks(folder)
		if err != nil {
			return nil, err
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("template %q leaves the profile's folder", name)
		}
		return readProfileFile(target, maxTemplateBytes)
	}
}

func readProfileFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- user-selected profile or checked template
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
