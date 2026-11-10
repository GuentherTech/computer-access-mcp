package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Save replaces a file only after the complete new version has reached disk.
func Save(path string, value any, secret bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if secret {
		data, err = seal(data)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".save-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Load(path string, value any, secret bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if secret {
		data, err = unseal(data)
		if err != nil {
			return fmt.Errorf("decrypt saved configuration: %w", err)
		}
	}
	return json.Unmarshal(data, value)
}

// Lock holds an OS lock until the returned function is called. Crashes release it.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("configuration is already in use: %w", err)
	}
	return func() { f.Close() }, nil
}
