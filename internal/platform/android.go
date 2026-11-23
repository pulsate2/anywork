// Package platform configures runtime platform integration before services start.
package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Init preserves user overrides. Go and native agents continue to use their
// normal DNS and proxy mechanisms; no hardcoded DNS or disabled TLS checking.
func Init() {
	if runtime.GOOS == "android" {
		initAndroid(os.Getenv, os.Setenv, regularFile, directoryExists)
	}
}

func initAndroid(getenv func(string) string, setenv func(string, string) error, fileExists, dirExists func(string) bool) {
	prefix := getenv("PREFIX")
	if prefix == "" {
		prefix = getenv("TERMUX__PREFIX")
	}
	if prefix != "" {
		cert := filepath.Join(prefix, "etc", "tls", "cert.pem")
		if fileExists(cert) {
			for _, key := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
				if getenv(key) == "" {
					_ = setenv(key, cert)
				}
			}
		}
	}
	if getenv("SSL_CERT_DIR") == "" {
		var dirs []string
		// Android 14+ also ships the system CA store in the Conscrypt APEX.
		for _, dir := range []string{"/apex/com.android.conscrypt/cacerts", "/system/etc/security/cacerts", "/data/misc/keychain/certs-added"} {
			if dirExists(dir) {
				dirs = append(dirs, dir)
			}
		}
		if len(dirs) > 0 {
			_ = setenv("SSL_CERT_DIR", strings.Join(dirs, string(os.PathListSeparator)))
		}
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
