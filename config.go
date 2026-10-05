package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultHost = "https://app.navigara.com"

type accountConfig struct {
	Version          int       `json:"version"`
	Host             string    `json:"host"`
	SessionToken     string    `json:"sessionToken,omitempty"`
	Email            string    `json:"email,omitempty"`
	UserID           string    `json:"userId,omitempty"`
	OrganizationID   string    `json:"organizationId,omitempty"`
	OrganizationName string    `json:"organizationName,omitempty"`
	IngestToken      string    `json:"ingestToken,omitempty"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
	IngestExpiresAt  time.Time `json:"ingestExpiresAt,omitempty"`
}

func canonicalHost(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\t\r\n ") {
		return "", errors.New("host must be an origin without whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.ContainsAny(raw, "?#") || u.Fragment != "" || u.RawFragment != "" {
		return "", errors.New("host must be an origin")
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" && u.RawPath != "/" {
		return "", errors.New("host must not include a path")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("host must use HTTPS")
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", errors.New("host must include a hostname")
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") || (port != "" && !validPort(port)) {
		return "", errors.New("host has an invalid port")
	}
	if scheme == "http" {
		ip := net.ParseIP(hostname)
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("HTTP hosts must be loopback IP addresses")
		}
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

func validPort(port string) bool {
	if port == "" {
		return false
	}
	value := 0
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return false
		}
		value = value*10 + int(digit-'0')
		if value > 65535 {
			return false
		}
	}
	return value > 0
}

func accountPath() (string, string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(base, "navigara")
	return dir, filepath.Join(dir, "config.json"), nil
}

func loadAccount(host string) (accountConfig, error) {
	wanted := defaultHost
	if host != "" {
		var err error
		wanted, err = canonicalHost(host)
		if err != nil {
			return accountConfig{}, err
		}
	}
	dir, path, err := accountPath()
	if err != nil {
		return accountConfig{}, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return accountConfig{Version: 1, Host: wanted}, nil
	}
	if err != nil {
		return accountConfig{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return accountConfig{}, fmt.Errorf("%s must be a private directory", dir)
	}
	if info.Mode().Perm() != 0700 {
		return accountConfig{}, fmt.Errorf("%s has insecure permissions", dir)
	}
	fileInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return accountConfig{Version: 1, Host: wanted}, nil
	}
	if err != nil {
		return accountConfig{}, err
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return accountConfig{}, fmt.Errorf("%s must be a regular file", path)
	}
	if fileInfo.Mode().Perm() != 0600 {
		return accountConfig{}, fmt.Errorf("%s has insecure permissions", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return accountConfig{}, err
	}
	var saved accountConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		return accountConfig{}, errors.New("invalid account config")
	}
	if saved.Version != 1 {
		return accountConfig{}, fmt.Errorf("unsupported account config version %d", saved.Version)
	}
	savedHost, err := canonicalHost(saved.Host)
	if err != nil || savedHost != saved.Host {
		return accountConfig{}, errors.New("account config has an invalid host")
	}
	if host != "" && saved.Host != wanted {
		return accountConfig{Version: 1, Host: wanted}, nil
	}
	return saved, nil
}

func saveAccount(c accountConfig) error {
	if c.Version != 0 && c.Version != 1 {
		return fmt.Errorf("unsupported account config version %d", c.Version)
	}
	host, err := canonicalHost(c.Host)
	if err != nil {
		return err
	}
	c.Version, c.Host = 1, host
	dir, path, err := accountPath()
	if err != nil {
		return err
	}
	if err := ensureAccountDir(dir); err != nil {
		return err
	}
	before, existed, err := accountSnapshot(path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".config.json-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0600)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	now, nowExists, err := accountSnapshot(path)
	if err != nil {
		return err
	}
	if nowExists != existed || !bytes.Equal(now, before) {
		return errors.New("account config changed during save; try again")
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func ensureAccountDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.Chmod(dir, 0700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("%s must be a private directory", dir)
	}
	return nil
}

func accountSnapshot(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s must be a regular file", path)
	}
	if info.Mode().Perm() != 0600 {
		return nil, false, fmt.Errorf("%s has insecure permissions", path)
	}
	b, err := os.ReadFile(path)
	return b, true, err
}
