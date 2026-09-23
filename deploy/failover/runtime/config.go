package runtime

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/piwi3910/nexora/deploy/failover/lease"
)

// FileConfig accepts parent-provisioned lab trust only, never discovers ambient
// kubeconfigs or prints token/certificate contents. Durations are nanoseconds.
type FileConfig struct {
	Mode                                         string
	Driver                                       lease.DriverOptions
	Stage                                        StageOptions
	Endpoint, Namespace, Name, TokenFile, CAFile string
	Margin, Interval, Duration                   time.Duration
}

func readBounded(path string, uid uint32, max int64) ([]byte, error) {
	if err := trusted(path, uid, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("configuration file unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, errors.New("configuration size/read failure")
	}
	return data, nil
}

// LoadConfig rejects unknown fields, duplicate keys and trailing JSON. It does
// not install trust, create a Lease, change clock assumptions or enable serving.
func LoadConfig(path string) (Config, error) { return loadConfig(path, false) }

// LoadActiveLabConfig is exclusively for the isolated active lab executable.
func LoadActiveLabConfig(path string) (Config, error) { return loadConfig(path, true) }

func loadConfig(path string, active bool) (Config, error) {
	uid := uint32(os.Geteuid())
	data, err := readBounded(path, uid, 32<<10)
	if err != nil {
		return Config{}, err
	}
	var file FileConfig
	if err = decodeConfig(data, &file); err != nil {
		return Config{}, err
	}
	cfg := Config{Mode: file.Mode, Driver: file.Driver, Stage: file.Stage, Margin: file.Margin, Interval: file.Interval, Duration: file.Duration}
	if active {
		if file.Endpoint != "https://127.0.0.1:6443" || file.Namespace != "fg-lab" || file.Name != "isolated" || cfg.Mode != "isolated-active-lab-v1" || cfg.Driver.Interface != "fg0" || cfg.Driver.LabBackendInterface != "bg0" || cfg.Driver.LabBackendAlias == "" {
			return Config{}, errors.New("exact isolated dual lab configuration required")
		}
		// Reuse scheduling validation only; detached Execute remains refused.
		schedule := cfg
		schedule.Mode = "detached-lab-v1"
		schedule.Driver.LabBackendInterface, schedule.Driver.LabBackendAlias = "", ""
		err = schedule.validate()
	} else {
		err = cfg.validate()
	}
	if err != nil {
		return Config{}, err
	}
	token, err := readBounded(file.TokenFile, uid, 16384)
	if err != nil {
		return Config{}, err
	}
	ca, err := readBounded(file.CAFile, uid, 64<<10)
	if err != nil {
		return Config{}, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return Config{}, errors.New("explicit lab CA required")
	}
	cfg.Authority = lease.HTTPSConfig{Endpoint: file.Endpoint, Namespace: file.Namespace, Name: file.Name, Token: string(token), Roots: roots, Timeout: file.Driver.IOTimeout}
	return cfg, nil
}

func decodeConfig(data []byte, out any) error {
	if !utf8.Valid(data) {
		return errors.New("configuration must be UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 8 {
			return errors.New("configuration nesting bound")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				s = strings.ToLower(s)
				if !ok || seen[s] {
					return errors.New("duplicate configuration key")
				}
				seen[s] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		} else if delimiter == '[' {
			for d.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		} else {
			return errors.New("invalid configuration delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return errors.New("invalid/ambiguous configuration")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing configuration")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("unsupported configuration fields/types")
	}
	return nil
}
