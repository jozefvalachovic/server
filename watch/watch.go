// Package watch provides a zero-dependency, in-process hot-reloader for
// development. It is designed to be called as the very first statement in
// main() and is a complete no-op in production.
//
// # How it works
//
// When DEV=1, the first process that calls Init() transforms itself into a
// file-system watcher. It compiles the service to a temporary binary via
// "go build", then runs that binary with _WATCH_CHILD=1 injected into its
// environment so the child's own Init() call returns immediately. Whenever
// a watched .go file changes, the binary is recompiled and the child is
// replaced. If compilation fails the current child keeps running and the
// build error is printed to stderr.
//
// Using "go build" (not "go run") means Go's build cache handles incremental
// compilation, so restarts after small changes are fast.
//
// # Usage
//
//	func main() {
//	    watch.Init() // no-op unless DEV=1
//	    // ... normal server setup
//	}
//
//	DEV=1 go run .   # watcher process compiles & manages the server child
//
// # Monorepo usage
//
// In a Go workspace (go.work) monorepo where multiple services share local
// packages, pass the shared module roots as ExtraDirs so a change anywhere
// in the workspace triggers a restart of the relevant service:
//
//	// services/myservice/main.go
//	watch.Init(watch.Config{
//	    ExtraDirs: []string{"../../shared", "../../pkg"},
//	})
//
// The working directory of the service is always watched automatically.
package watch

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	devEnvKey   = "DEV"
	childEnvKey = "_WATCH_CHILD"
)

// Config customises the watcher. All fields are optional.
type Config struct {
	// Extra directories to watch in addition to the working directory.
	// Use this in a monorepo to watch shared/pkg modules that the service
	// imports. The cwd of the service is always included automatically.
	ExtraDirs []string

	// Additional flags passed to "go build" (e.g. []string{"-tags", "dev"}).
	BuildArgs []string

	// File extension to watch. Defaults to ".go".
	Ext string

	// How often to check for changes. Defaults to 500 ms.
	Interval time.Duration
}

// Init starts the hot-reload watcher when DEV=1.
// Call it as the very first statement in main(); it returns immediately in
// every other situation (production, already a managed child).
func Init(cfgs ...Config) {
	if os.Getenv(devEnvKey) != "1" {
		return // production — skip entirely
	}
	if os.Getenv(childEnvKey) == "1" {
		return // we are already a managed child; let main() continue
	}

	cfg := Config{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.Ext == "" {
		cfg.Ext = ".go"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 500 * time.Millisecond
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("watch: getwd: %v", err)
	}

	// Derive the build directory from the file that called Init() so that
	// "go build ." targets the correct main package even when the service
	// is launched from a parent directory (e.g. "go run ./example" from
	// the repo root).
	buildDir := cwd
	if _, callerFile, _, ok := runtime.Caller(1); ok && filepath.IsAbs(callerFile) {
		buildDir = filepath.Dir(callerFile)
	}

	// Temp binary named after the build directory to avoid collisions.
	binPath := filepath.Join(os.TempDir(), "watch-"+filepath.Base(buildDir))

	dirs := append([]string{buildDir}, cfg.ExtraDirs...)

	log.Printf("watch: DEV mode — watching %s (%s) every %s",
		strings.Join(dirs, ", "), cfg.Ext, cfg.Interval)

	runWatcher(dirs, cfg.Ext, cfg.Interval, cfg.BuildArgs, binPath, buildDir)
	// Clean up the temp binary on exit.
	_ = os.Remove(binPath)
	os.Exit(0)
}

// ── watcher loop ──────────────────────────────────────────────────────────────

func runWatcher(dirs []string, ext string, interval time.Duration, buildArgs []string, binPath, cwd string) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	m := &manager{binPath: binPath, cwd: cwd}

	// Initial build + start. If the first build fails, keep retrying on
	// every change so the developer can fix compile errors and see the
	// server come up automatically.
	if err := m.build(buildArgs); err != nil {
		log.Printf("watch: initial build failed:\n%s", err)
	} else {
		m.start()
	}

	last := fingerprint(dirs, ext)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cur := fingerprint(dirs, ext)
			if cur == last {
				continue
			}
			last = cur
			log.Println("watch: change detected — rebuilding …")

			if err := m.build(buildArgs); err != nil {
				// Build failed — print error, leave current child running.
				log.Printf("watch: build failed (server still running):\n%s", err)
				continue
			}
			log.Println("watch: build OK — restarting …")
			m.restart()

		case sig := <-quit:
			log.Printf("watch: received %s — shutting down …", sig)
			m.stop()
			return
		}
	}
}

// ── fingerprint ───────────────────────────────────────────────────────────────

func fingerprint(dirs []string, ext string) string {
	h := sha256.New()
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if d.IsDir() || filepath.Ext(path) != ext {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			_, _ = fmt.Fprintf(h, "%s:%d", path, fi.ModTime().UnixNano())
			return nil
		})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ── child process manager ─────────────────────────────────────────────────────

type manager struct {
	binPath string
	cwd     string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// build compiles the service into binPath. Returns the combined output on
// failure so callers can log it.
func (m *manager) build(extraArgs []string) error {
	args := append([]string{"build", "-o", m.binPath}, extraArgs...)
	args = append(args, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = m.cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Chmod(m.binPath, 0755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}
	return nil
}

func (m *manager) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launch()
}

func (m *manager) launch() {
	cmd := exec.Command(m.binPath)
	cmd.Dir = m.cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	// Inject _WATCH_CHILD=1 so the child's Init() is a no-op.
	cmd.Env = append(os.Environ(), childEnvKey+"=1")
	// Own process group so we can kill all descendants cleanly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		log.Printf("watch: launch: %v", err)
		return
	}
	m.cmd = cmd
	go func() { _ = cmd.Wait() }()
}

func (m *manager) kill() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(m.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan struct{})
	go func() { _ = m.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = m.cmd.Process.Kill()
		}
	}
	m.cmd = nil
}

func (m *manager) restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kill()
	m.launch()
}

func (m *manager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kill()
}
