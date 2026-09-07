package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lazymc/proto"
)

const maxForgeStatusFileSize = 1 << 20

func loadForgeStatusSnapshot(config *Config) (*proto.ServerStatus, error) {
	path, err := forgeStatusPath(config)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxForgeStatusFileSize {
		return nil, errors.New("Forge status snapshot is not a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err = validateForgeStatusData(raw)
	if err != nil {
		return nil, err
	}
	return &proto.ServerStatus{
		Version:   proto.ServerVersion{Name: config.Public.Version, Protocol: config.Public.Protocol},
		Players:   proto.OnlinePlayers{Sample: []proto.OnlinePlayer{}},
		ForgeData: raw,
	}, nil
}

func forgeStatusPath(config *Config) (string, error) {
	path := config.Server.ForgeStatusFile
	if path == "" {
		return "", errors.New("Forge status snapshot path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(config.Path), path)
	}
	return filepath.Clean(path), nil
}

func validateForgeStatusData(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxForgeStatusFileSize {
		return nil, errors.New("Forge status snapshot is outside its size bound")
	}
	raw = bytes.TrimSpace(raw)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid Forge status JSON: %w", err)
	}
	if len(object) == 0 || len(object["fmlNetworkVersion"]) == 0 || len(object["channels"]) == 0 {
		return nil, errors.New("Forge status snapshot lacks required metadata")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func persistForgeStatusSnapshot(config *Config, raw []byte) error {
	validated, err := validateForgeStatusData(raw)
	if err != nil {
		return err
	}
	path, err := forgeStatusPath(config)
	if err != nil {
		return err
	}
	if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(bytes.TrimSpace(current), validated) {
		return nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".forge-status-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(validated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
