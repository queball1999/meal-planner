// Command build_sidecar compiles Go Eat for the desktop app's Tauri shell,
// under the name Tauri's externalBin expects:
// desktop/src-tauri/binaries/goeat-<rust target triple>[.exe].
//
// It runs as the shell's beforeDevCommand/beforeBuildCommand (see
// desktop/src-tauri/tauri.conf.json), so `npm run tauri build` in desktop/
// always bundles a fresh server. The target triple comes from
// TAURI_ENV_TARGET_TRIPLE when Tauri sets it, else from `rustc -vV`.
//
// When the branded QUpdateTool updater has been placed in
// desktop/src-tauri/updater/ (CI does this; see build_updater.yaml), its
// SHA-256 is stamped into the binary as main.updaterSHA256, so the app can
// refuse to launch an updater that has been swapped since (phase 15).
//
//	go run ./cmd/build_sidecar [-version v1.2.3]
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// goTargets maps the Rust triples we ship (Windows + Linux, amd64 + arm64)
// to GOOS/GOARCH.
var goTargets = map[string][2]string{
	"x86_64-pc-windows-msvc":    {"windows", "amd64"},
	"aarch64-pc-windows-msvc":   {"windows", "arm64"},
	"x86_64-pc-windows-gnu":     {"windows", "amd64"},
	"x86_64-unknown-linux-gnu":  {"linux", "amd64"},
	"aarch64-unknown-linux-gnu": {"linux", "arm64"},
}

func main() {
	version := flag.String("version", os.Getenv("GOEAT_VERSION"), "version stamped into the binary (main.version)")
	flag.Parse()
	if *version == "" {
		*version = "desktop-dev"
	}

	triple := os.Getenv("TAURI_ENV_TARGET_TRIPLE")
	if triple == "" {
		var err error
		if triple, err = hostTriple(); err != nil {
			log.Fatalf("build_sidecar: %v", err)
		}
	}
	target, ok := goTargets[triple]
	if !ok {
		log.Fatalf("build_sidecar: unsupported target %q", triple)
	}

	root, err := moduleRoot()
	if err != nil {
		log.Fatalf("build_sidecar: %v", err)
	}
	name := "goeat-" + triple
	if target[0] == "windows" {
		name += ".exe"
	}
	out := filepath.Join(root, "desktop", "src-tauri", "binaries", name)
	// binaries/ holds only build output (git-ignored), so a fresh clone has none.
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatalf("build_sidecar: %v", err)
	}

	ldflags := "-s -w -X main.version=" + *version
	updater := filepath.Join(root, "desktop", "src-tauri", "updater", "updater")
	if target[0] == "windows" {
		updater += ".exe"
	}
	switch sum, err := fileSHA256(updater); {
	case err == nil:
		ldflags += " -X main.updaterSHA256=" + sum
		fmt.Fprintf(os.Stderr, "build_sidecar: bundled updater %s sha256 %s\n", updater, sum)
	case os.IsNotExist(err):
		fmt.Fprintf(os.Stderr, "build_sidecar: no updater at %s; this build can't install updates itself\n", updater)
	default:
		log.Fatalf("build_sidecar: hash updater: %v", err)
	}

	cmd := exec.Command("go", "build", "-trimpath",
		"-ldflags", ldflags,
		"-o", out, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target[0], "GOARCH="+target[1])
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("build_sidecar: go build: %v", err)
	}
	fmt.Fprintf(os.Stderr, "build_sidecar: built %s (%s)\n", out, *version)
}

// fileSHA256 is the hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// hostTriple reads the host target triple from `rustc -vV`.
func hostTriple() (string, error) {
	out, err := exec.Command("rustc", "-vV").Output()
	if err != nil {
		return "", fmt.Errorf("rustc -vV: %w (is Rust installed?)", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "host: "); ok {
			return t, nil
		}
	}
	return "", fmt.Errorf("no host: line in rustc -vV output")
}

// moduleRoot is the directory holding go.mod, wherever this runs from.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := string(bytes.TrimSpace(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("not inside the goeat module")
	}
	return filepath.Dir(gomod), nil
}
