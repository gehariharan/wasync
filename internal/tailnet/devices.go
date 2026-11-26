package tailnet

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/passkey"
	"github.com/gehariharan/wasync/internal/processlock"
)

// Devices that may confirm owner actions from another computer.
//
// Each one is a passkey the owner registered on that device, after the
// computer running wasync handed out a one-time code. The private file keeps
// only public keys. A device belongs to the host and login it was registered
// for and stops counting as soon as either changes; `wasync tailnet off`
// deletes them all.

const (
	// DevicesFileName is the private list of registered devices.
	DevicesFileName = "tailnet-devices.json"
	devicesLockName = "tailnet-devices.lock"
	// MaxDevices is how many devices may be registered at once.
	MaxDevices = 20
	// MaxDeviceName is the longest device name kept, in characters.
	MaxDeviceName = 40
)

// ErrTooManyDevices means MaxDevices are registered already.
var ErrTooManyDevices = fmt.Errorf("at most %d devices can be set up; remove one first", MaxDevices)

// Device is one registered passkey.
type Device struct {
	// ID is the passkey's credential ID, base64url without padding.
	ID        string     `json:"id"`
	PublicKey []byte     `json:"public_key"` // SubjectPublicKeyInfo, DER
	Alg       int        `json:"alg"`
	SignCount uint32     `json:"sign_count"`
	Host      string     `json:"host"`
	Login     string     `json:"login"`
	Name      string     `json:"name"`
	AddedAt   time.Time  `json:"added_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

// Key is the device's passkey in the form passkey.VerifyAssertion checks.
func (d Device) Key() (passkey.Key, error) {
	id, err := base64.RawURLEncoding.DecodeString(d.ID)
	if err != nil {
		return passkey.Key{}, err
	}
	return passkey.Key{ID: id, PublicKey: d.PublicKey, Alg: d.Alg, SignCount: d.SignCount}, nil
}

func (d Device) validate() error {
	id, err := base64.RawURLEncoding.DecodeString(d.ID)
	if err != nil || len(id) == 0 || len(id) > passkey.MaxIDLength {
		return errors.New("device ID")
	}
	if len(d.PublicKey) == 0 || len(d.PublicKey) > 2048 || !slices.Contains(passkey.Algorithms, d.Alg) {
		return errors.New("device key")
	}
	if host, err := CanonicalHost(d.Host); err != nil || host != d.Host {
		return errors.New("device host")
	}
	if err := validLogin(d.Login); err != nil {
		return errors.New("device login")
	}
	if len([]rune(d.Name)) > MaxDeviceName {
		return errors.New("device name")
	}
	return nil
}

// For is the devices registered for this setting's host and login.
func (s Setting) For(all []Device) []Device {
	var out []Device
	for _, d := range all {
		if d.Host == s.Host && d.Login == s.Login {
			out = append(out, d)
		}
	}
	return out
}

type devicesFile struct {
	Devices []Device `json:"devices"`
}

// LoadDevices returns every registered device. A missing file is none; a
// damaged or non-private file is an error, which callers treat as none.
func LoadDevices(dir string) ([]Device, error) {
	if dir == "" {
		return nil, nil
	}
	path := filepath.Join(dir, DevicesFileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file: %w", DevicesFileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return nil, err
	}
	var file devicesFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", DevicesFileName, err)
	}
	seen := map[string]bool{}
	for _, d := range file.Devices {
		if err := d.validate(); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", DevicesFileName, err)
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("invalid %s: a device is listed twice", DevicesFileName)
		}
		seen[d.ID] = true
	}
	return file.Devices, nil
}

// AddDevice registers a device. A passkey registered before is refused.
func AddDevice(dir string, d Device) error {
	if err := d.validate(); err != nil {
		return err
	}
	return updateDevices(dir, func(list []Device) ([]Device, error) {
		if len(list) >= MaxDevices {
			return nil, ErrTooManyDevices
		}
		for _, old := range list {
			if old.ID == d.ID {
				return nil, errors.New("this passkey is already set up")
			}
		}
		return append(list, d), nil
	})
}

// RemoveDevice forgets one device; found is false when there was none with that ID.
func RemoveDevice(dir, id string) (removed Device, found bool, err error) {
	err = updateDevices(dir, func(list []Device) ([]Device, error) {
		i := slices.IndexFunc(list, func(d Device) bool { return d.ID == id })
		if i < 0 {
			return list, nil
		}
		removed, found = list[i], true
		return slices.Delete(list, i, i+1), nil
	})
	return removed, found, err
}

// UsedDevice records a confirmation: when, and the passkey's new signature counter.
func UsedDevice(dir, id string, count uint32, now time.Time) error {
	return updateDevices(dir, func(list []Device) ([]Device, error) {
		for i := range list {
			if list[i].ID == id {
				at := now.UTC().Truncate(time.Second)
				list[i].SignCount, list[i].UsedAt = count, &at
			}
		}
		return list, nil
	})
}

// KeepDevices forgets every device not registered for s, so devices set up
// for an earlier host or login do not come back if it is allowed again.
func KeepDevices(dir string, s Setting) error {
	return updateDevices(dir, func(list []Device) ([]Device, error) {
		return s.For(list), nil
	})
}

// RemoveDevices forgets every device. Removing none is not an error.
func RemoveDevices(dir string) error {
	err := os.Remove(filepath.Join(dir, DevicesFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// updateDevices changes the list under a lock shared with other wasync
// processes (the owner registers devices, the CLI removes them) and replaces
// the file atomically. A damaged file is replaced only by removing it.
func updateDevices(dir string, change func([]Device) ([]Device, error)) (result error) {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	lock, err := lockDevices(filepath.Join(dir, devicesLockName))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	list, err := LoadDevices(dir)
	if err != nil {
		return err
	}
	list, err = change(list)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return RemoveDevices(dir)
	}
	data, err := json.MarshalIndent(devicesFile{Devices: list}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(dir, DevicesFileName, append(data, '\n'))
}

func lockDevices(path string) (*processlock.Lock, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := processlock.Acquire(path)
		if !errors.Is(err, processlock.ErrBusy) {
			return lock, err
		}
		if time.Now().After(deadline) {
			return nil, errors.New("another wasync process is updating tailnet devices; retry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
