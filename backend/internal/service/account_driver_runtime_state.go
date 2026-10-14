package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type accountDriverRuntimeState struct {
	SchemaVersion int                           `json:"schema_version"`
	Generation    uint64                        `json:"generation"`
	Active        accountDriverRuntimeArtifact  `json:"active"`
	Previous      *accountDriverRuntimeArtifact `json:"previous,omitempty"`
}

func accountDriverStatePath(cfg *config.Config) string {
	root := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if root == "" {
		root = "./data"
	}
	if cfg != nil && strings.TrimSpace(cfg.Plugins.DataDir) != "" {
		root = cfg.Plugins.DataDir
	}
	path, _ := filepath.Abs(filepath.Join(root, "account-drivers", "codex-rs-runtime.json"))
	return path
}
func loadAccountDriverRuntimeState(path string) (*accountDriverRuntimeState, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("published account driver state is unreadable")
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 65537))
	decoder.DisallowUnknownFields()
	var record accountDriverRuntimeState
	if err := decoder.Decode(&record); err != nil {
		return nil, errors.New("published account driver state is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("published account driver state has trailing data")
	}
	if record.SchemaVersion != 1 || record.Generation == 0 || !validRuntimeArtifact(record.Active) || record.Previous != nil && !validRuntimeArtifact(*record.Previous) {
		return nil, errors.New("published account driver state is incompatible")
	}
	return &record, nil
}
func validRuntimeArtifact(artifact accountDriverRuntimeArtifact) bool {
	if !filepath.IsAbs(artifact.ExecutablePath) || len(artifact.SHA256) != 64 {
		return false
	}
	_, err := hex.DecodeString(artifact.SHA256)
	return err == nil
}
func saveAccountDriverRuntimeState(path string, record accountDriverRuntimeState) error {
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("account driver publication state could not be encoded")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("account driver publication directory could not be created")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".codex-runtime-*.tmp")
	if err != nil {
		return errors.New("account driver publication temporary file could not be created")
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("account driver publication state could not be saved")
	}
	if err := replaceAccountDriverStateFile(temp, path); err != nil {
		return errors.New("account driver publication state could not be committed")
	}
	return nil
}

// 每个已启动包都保存独立的按 hash 命名快照，原始路径被覆盖也不破坏回滚
func stageAccountDriverRuntimeArtifact(statePath string, artifact accountDriverRuntimeArtifact) (accountDriverRuntimeArtifact, error) {
	sourcePath, err := filepath.Abs(strings.TrimSpace(artifact.ExecutablePath))
	if err != nil || strings.TrimSpace(artifact.ExecutablePath) == "" {
		return artifact, errors.New("account driver executable is required")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return artifact, errors.New("account driver executable is unavailable")
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return artifact, errors.New("account driver executable is not a regular file")
	}
	root := filepath.Join(filepath.Dir(statePath), "artifacts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return artifact, errors.New("account driver artifact directory could not be created")
	}
	temp, err := os.CreateTemp(root, ".driver-*.tmp")
	if err != nil {
		return artifact, errors.New("account driver artifact could not be staged")
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	digest := sha256.New()
	_, err = io.Copy(io.MultiWriter(temp, digest), source)
	if err == nil {
		err = temp.Chmod(0555)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return artifact, errors.New("account driver artifact copy failed")
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	if artifact.SHA256 != "" && !strings.EqualFold(artifact.SHA256, hash) {
		return artifact, errors.New("account driver executable checksum does not match")
	}
	name := "driver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	directory := filepath.Join(root, hash)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return artifact, errors.New("account driver snapshot directory could not be created")
	}
	target := filepath.Join(directory, name)
	if existing, err := os.Open(target); err == nil {
		other := sha256.New()
		_, copyErr := io.Copy(other, existing)
		_ = existing.Close()
		if copyErr != nil || hex.EncodeToString(other.Sum(nil)) != hash {
			return artifact, errors.New("cached account driver artifact is corrupted")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tempPath, target); err != nil {
			return artifact, fmt.Errorf("account driver snapshot could not be installed: %w", err)
		}
	} else {
		return artifact, errors.New("cached account driver artifact is unavailable")
	}
	artifact.ExecutablePath = target
	artifact.SHA256 = hash
	return artifact, nil
}
