package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

type Config struct {
	PinCode           string `json:"pin_code"`
	HTTPPort          int    `json:"http_port"`
	DiscoveryPort     int    `json:"discovery_port"`
	TargetFPS         int    `json:"target_fps"`
	BitrateKbps       int    `json:"bitrate_kbps"`
	FilterVirtualNICs bool   `json:"filter_virtual_nics"`
	MacAddress        string `json:"mac_address"`
	LogMaxBytes       int64  `json:"log_max_bytes"`
	LogMaxFiles       int    `json:"log_max_files"`
}

func DefaultConfig() Config {
	return Config{
		PinCode:           generatePin6(),
		HTTPPort:          9527,
		DiscoveryPort:     9528,
		TargetFPS:         60,
		BitrateKbps:       20000,
		FilterVirtualNICs: true,
		LogMaxBytes:       5 * 1024 * 1024,
		LogMaxFiles:       5,
	}
}

func generatePin6() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "952700"
	}
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

// ResolveBaseDir locates the ZeroDesk root directory strictly relative to the binary or working directory.
func ResolveBaseDir() (string, error) {
	if envRoot := os.Getenv("ZERODESK_ROOT"); envRoot != "" {
		return filepath.Abs(envRoot)
	}
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		if _, statErr := os.Stat(filepath.Join(exeDir, "data")); statErr == nil {
			return exeDir, nil
		}
		parent := filepath.Dir(exeDir)
		if _, statErr := os.Stat(filepath.Join(parent, "data")); statErr == nil {
			return parent, nil
		}
		if filepath.Base(exeDir) == "ZeroDesk" {
			return exeDir, nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(filepath.Join(wd, "data")); statErr == nil {
		return wd, nil
	}
	parent := filepath.Dir(wd)
	if _, statErr := os.Stat(filepath.Join(parent, "data")); statErr == nil {
		return parent, nil
	}
	return wd, nil
}

// LoadOrCreate loads data/config.json under baseDir or creates it with defaults.
func LoadOrCreate(baseDir string) (Config, string, error) {
	dataDir := filepath.Join(baseDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return Config{}, "", err
	}
	cfgPath := filepath.Join(dataDir, "config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if saveErr := Save(cfgPath, cfg); saveErr != nil {
				return cfg, cfgPath, saveErr
			}
			return cfg, cfgPath, nil
		}
		return Config{}, cfgPath, err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, cfgPath, fmt.Errorf("invalid config.json: %w", err)
	}
	changed := false
	if len(cfg.PinCode) != 6 {
		cfg.PinCode = generatePin6()
		changed = true
	}
	if cfg.HTTPPort <= 0 {
		cfg.HTTPPort = 9527
		changed = true
	}
	if cfg.DiscoveryPort <= 0 {
		cfg.DiscoveryPort = 9528
		changed = true
	}
	if cfg.TargetFPS <= 0 {
		cfg.TargetFPS = 60
		changed = true
	}
	if cfg.BitrateKbps <= 0 {
		cfg.BitrateKbps = 20000
		changed = true
	}
	if cfg.LogMaxBytes <= 0 {
		cfg.LogMaxBytes = 5 * 1024 * 1024
		changed = true
	}
	if cfg.LogMaxFiles <= 0 {
		cfg.LogMaxFiles = 5
		changed = true
	}
	if changed {
		_ = Save(cfgPath, cfg)
	}
	return cfg, cfgPath, nil
}

func Save(cfgPath string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, b, 0644)
}
