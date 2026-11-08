package config

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ServeConfig describes one operational mode to run through the serve command.
type ServeConfig struct {
	Mode    string         `json:"mode"`
	Options map[string]any `json:"-"`
}

// UnmarshalJSON supports the serve config syntax:
//
//  "serve": [{
//    "mode": "socks",
//    "bind": "0.0.0.0",
//    "port": 1080
//  }]
//
// It also accepts a nested "options" object for compatibility with generic config documents.
func (s *ServeConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	s.Options = make(map[string]any)

	if value, ok := raw["mode"]; ok {
		if err := json.Unmarshal(value, &s.Mode); err != nil {
			return err
		}
	}

	if value, ok := raw["options"]; ok {
		var nested map[string]any
		if err := json.Unmarshal(value, &nested); err != nil {
			return err
		}
		for key, val := range nested {
			s.Options[key] = val
		}
	}

	for key, value := range raw {
		if key == "mode" || key == "options" {
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		s.Options[key] = decoded
	}

	return nil
}

// NormalizedMode returns a canonical mode name accepted by the CLI.
func (s ServeConfig) NormalizedMode() (string, error) {
	mode := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(s.Mode, "_", "-")))
	switch mode {
	case "http-proxy", "httpproxy":
		return "http-proxy", nil
	case "l4-http-proxy", "l4-httpproxy":
		return "l4-http-proxy", nil
	case "l4-socks", "l4socks":
		return "l4-socks", nil
	case "nativetun", "native-tun":
		return "nativetun", nil
	case "portfw", "port-fw":
		return "portfw", nil
	case "socks":
		return "socks", nil
	case "":
		return "", fmt.Errorf("serve mode is required")
	default:
		return "", fmt.Errorf("unsupported serve mode %q", s.Mode)
	}
}

// CLIArgs converts the configured serve entry into the arguments required for the CLI subcommand.
func (s ServeConfig) CLIArgs() []string {
	mode, err := s.NormalizedMode()
	if err != nil || mode == "" {
		return nil
	}

	args := []string{mode}
	keys := make([]string, 0, len(s.Options))
	for key := range s.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if key == "mode" || key == "options" {
			continue
		}

		flag := "--" + strings.ReplaceAll(strings.ToLower(key), "_", "-")
		value := s.Options[key]
		if value == nil {
			continue
		}

		switch v := value.(type) {
		case bool:
			if v {
				args = append(args, flag)
			}
		case []any:
			for _, item := range v {
				args = append(args, flag, fmt.Sprint(item))
			}
		case []string:
			for _, item := range v {
				args = append(args, flag, item)
			}
		case string:
			args = append(args, flag, v)
		default:
			args = append(args, flag, fmt.Sprint(v))
		}
	}

	if rawArgs, ok := s.Options["args"]; ok {
		switch v := rawArgs.(type) {
		case []any:
			for _, item := range v {
				args = append(args, fmt.Sprint(item))
			}
		case []string:
			for _, item := range v {
				args = append(args, item)
			}
		case string:
			args = append(args, strings.Fields(v)...)
		}
	}

	return args
}

// Config represents the application configuration structure, containing essential details such as keys, endpoints, and access tokens.
type Config struct {
	PrivateKey     string        `json:"private_key"`      // Base64-encoded ECDSA private key
	EndpointV4     string        `json:"endpoint_v4"`      // IPv4 address of the endpoint
	EndpointV6     string        `json:"endpoint_v6"`      // IPv6 address of the endpoint
	EndpointH2V4   string        `json:"endpoint_h2_v4"`   // IPv4 address used in HTTP/2 mode
	EndpointH2V6   string        `json:"endpoint_h2_v6"`   // IPv6 address used in HTTP/2 mode
	EndpointPubKey string        `json:"endpoint_pub_key"` // PEM-encoded ECDSA public key of the endpoint to verify against
	ID             string        `json:"id"`               // Device unique identifier
	AccessToken    string        `json:"access_token"`     // Authentication token for API access
	IPv4           string        `json:"ipv4"`             // Assigned IPv4 address
	IPv6           string        `json:"ipv6"`             // Assigned IPv6 address
	Serve          []ServeConfig `json:"serve,omitempty"`  // Zero or more operational modes to run concurrently
}

// AppConfig holds the global application configuration.
var AppConfig Config

// ConfigLoaded indicates whether the configuration has been successfully loaded.
var ConfigLoaded bool

// LoadConfig loads the application configuration from a JSON file.
//
// Parameters:
//   - configPath: string - The path to the configuration JSON file.
//
// Returns:
//   - error: An error if the configuration file cannot be loaded or parsed.
func LoadConfig(configPath string) error {
	file, err := os.Open(configPath)
	if err != nil {
		return fmt.Errorf("failed to open config file: %v", err)
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&AppConfig); err != nil {
		return fmt.Errorf("failed to decode config file: %v", err)
	}

	ConfigLoaded = true

	return nil
}

// SaveConfig writes the current application configuration to a prettified JSON file.
//
// Parameters:
//   - configPath: string - The path to save the configuration JSON file.
//
// Returns:
//   - error: An error if the configuration file cannot be written.
func (*Config) SaveConfig(configPath string) error {
	file, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("failed to create config file: %v", err)
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(AppConfig); err != nil {
		return fmt.Errorf("failed to encode config file: %v", err)
	}

	return nil
}

// GetEcPrivateKey retrieves the ECDSA private key from the stored Base64-encoded string.
//
// Returns:
//   - *ecdsa.PrivateKey: The parsed ECDSA private key.
//   - error: An error if decoding or parsing the private key fails.
func (*Config) GetEcPrivateKey() (*ecdsa.PrivateKey, error) {
	privKeyB64, err := base64.StdEncoding.DecodeString(AppConfig.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key: %v", err)
	}

	privKey, err := x509.ParseECPrivateKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %v", err)
	}

	return privKey, nil
}

// GetEcEndpointPublicKey retrieves the ECDSA public key from the stored PEM-encoded string.
//
// Returns:
//   - *ecdsa.PublicKey: The parsed ECDSA public key.
//   - error: An error if decoding or parsing the public key fails.
func (*Config) GetEcEndpointPublicKey() (*ecdsa.PublicKey, error) {
	endpointPubKeyB64, _ := pem.Decode([]byte(AppConfig.EndpointPubKey))
	if endpointPubKeyB64 == nil {
		return nil, fmt.Errorf("failed to decode endpoint public key")
	}

	pubKey, err := x509.ParsePKIXPublicKey(endpointPubKeyB64.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	ecPubKey, ok := pubKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to assert public key as ECDSA")
	}

	return ecPubKey, nil
}

