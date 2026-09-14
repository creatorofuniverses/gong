package cli

import (
	"errors"
	"flag"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/creatorofuniverses/gong/internal/config"
)

// discoverConfig chooses one file; files are never merged. A missing explicit
// path is handled by Load, while a missing default configuration is optional
// for clients. Invalid or unreadable discovered files must not silently fall
// back to another address or lose their API token.
func discoverConfig(explicit string, provided bool) (string, error) {
	if provided {
		if strings.TrimSpace(explicit) == "" {
			return "", errors.New("--config must not be empty")
		}
		return explicit, nil
	}
	paths := []string{"gong.yaml"}
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".config")
		} else {
			base = ""
		}
	}
	if base != "" {
		paths = append(paths, filepath.Join(base, "gong", "config.yaml"))
	}
	for _, path := range paths {
		_, err := os.Stat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("cannot inspect configuration file")
		}
	}
	return "", nil
}

func resolveClientConfig(fs *flag.FlagSet, values *clientFlags) error {
	present := visitedFlags(fs)
	path, err := discoverConfig(values.configPath, present["config"])
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	cfg, err := config.LoadClient(path)
	if err != nil {
		return errors.New("could not load config; check the file path and YAML settings")
	}
	if !present["url"] && os.Getenv("GONG_URL") == "" {
		values.baseURL, err = localGatewayURL(cfg.Listen)
		if err != nil {
			return err
		}
	}
	if !present["token"] && os.Getenv("GONG_API_TOKEN") == "" {
		values.token = cfg.APIToken
	}
	return nil
}

// listen has already been validated by config.LoadClient. Configuration files
// can select only local destinations; remote gateways require an explicit URL.
func localGatewayURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", errors.New("config listen must be a valid TCP host:port address")
	}
	if host == "" || strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		host = "127.0.0.1"
	} else {
		// A scope is not needed for loopback or unspecified addresses. Removing
		// it also lets every equivalent IP spelling use the same local check.
		address, _, _ := strings.Cut(host, "%")
		ip := net.ParseIP(address)
		if ip == nil || !ip.IsLoopback() && !ip.IsUnspecified() {
			return "", errors.New("config listen host is not local; set --url or GONG_URL explicitly to choose a remote gateway")
		}
		host = ip.String()
		if ip.IsUnspecified() {
			if ip.To4() != nil {
				host = "127.0.0.1"
			} else {
				host = "::1"
			}
		}
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}
