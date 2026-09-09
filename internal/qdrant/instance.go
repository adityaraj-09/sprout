package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adityaraj/sprout/internal/postgres"
)

const engineMarker = ".sprout-engine"

type Binaries struct {
	Qdrant string
}

func LookBinaries() (Binaries, error) {
	p, err := exec.LookPath("qdrant")
	if err != nil {
		return Binaries{}, fmt.Errorf("missing qdrant in PATH (install the Qdrant server binary)")
	}
	return Binaries{Qdrant: p}, nil
}

func FindOnPath() Binaries {
	p, _ := exec.LookPath("qdrant")
	return Binaries{Qdrant: p}
}

type Instance struct {
	Name     string
	Source   string
	Owner    string
	DataDir  string
	Port     int
	LogFile  string
	Password string
	Bins     Binaries
	cmd      *exec.Cmd
}

func (i *Instance) pidFile() string    { return filepath.Join(i.DataDir, "qdrant.pid") }
func (i *Instance) confFile() string   { return filepath.Join(i.DataDir, "config.yaml") }
func (i *Instance) certFile() string   { return filepath.Join(i.DataDir, "cert.pem") }
func (i *Instance) keyFile() string    { return filepath.Join(i.DataDir, "key.pem") }
func (i *Instance) storageDir() string { return filepath.Join(i.DataDir, "storage") }
func (i *Instance) snapDir() string    { return filepath.Join(i.DataDir, "snapshots") }
func (i *Instance) markerFile() string { return filepath.Join(i.DataDir, engineMarker) }

func HasDataDir(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err == nil {
		return false
	}
	if b, err := os.ReadFile(filepath.Join(dir, engineMarker)); err == nil {
		if strings.TrimSpace(string(b)) == "qdrant" {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		if body, rerr := os.ReadFile(filepath.Join(dir, "config.yaml")); rerr == nil && strings.Contains(string(body), "storage_path:") {
			return true
		}
	}
	for _, name := range []string{
		filepath.Join(dir, "storage", "raft_state.json"),
		filepath.Join(dir, "storage", "alias"),
		filepath.Join(dir, "storage", "collections"),
	} {
		if _, err := os.Stat(name); err == nil {
			return true
		}
	}
	return false
}

func (i *Instance) Init() error {
	if err := os.MkdirAll(i.storageDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(i.snapDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(i.markerFile(), []byte("qdrant\n"), 0o600); err != nil {
		return err
	}
	return i.writeConfig()
}

func (i *Instance) writeConfig() error {
	if i.LogFile == "" {
		i.LogFile = filepath.Join(i.DataDir, "qdrant.log")
	}
	if err := os.MkdirAll(filepath.Dir(i.LogFile), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(i.storageDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(i.snapDir(), 0o700); err != nil {
		return err
	}
	tlsBlock := ""
	enableTLS := "false"
	if useTLS() {
		if err := writeTLSFiles(i.certFile(), i.keyFile()); err != nil {
			return err
		}
		enableTLS = "true"
		tlsBlock = fmt.Sprintf("tls:\n  cert: %s\n  key: %s\n", yamlStr(i.certFile()), yamlStr(i.keyFile()))
	}
	apiKey := ""
	if i.Password != "" {
		apiKey = fmt.Sprintf("  api_key: %s\n", yamlStr(i.Password))
	}
	grpcPort, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`log_level: INFO
telemetry_disabled: true
storage:
  storage_path: %s
  snapshots_path: %s
service:
  host: %s
  http_port: %d
  grpc_port: %d
  enable_tls: %s
  max_request_size_mb: 1024
%s%s`, yamlStr(i.storageDir()), yamlStr(i.snapDir()), yamlStr(bindIP()), i.Port, grpcPort, enableTLS, apiKey, tlsBlock)
	return os.WriteFile(i.confFile(), []byte(body), 0o600)
}

func yamlStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(b)
}

func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (i *Instance) PrepareClone() error {
	_ = os.Remove(i.pidFile())
	_ = os.Remove(filepath.Join(i.storageDir(), ".lock"))
	_ = os.Remove(filepath.Join(i.DataDir, ".lock"))
	return i.writeConfig()
}

func (i *Instance) Start() error {
	if i.LogFile == "" {
		i.LogFile = filepath.Join(i.DataDir, "qdrant.log")
	}
	if err := i.writeConfig(); err != nil {
		return err
	}
	if i.pidAlive() {
		return nil
	}
	if err := postgres.EnsurePortFree(i.Port); err != nil {
		return err
	}
	if i.Bins.Qdrant == "" {
		i.Bins = FindOnPath()
	}
	if i.Bins.Qdrant == "" {
		return fmt.Errorf("missing qdrant in PATH (install the Qdrant server binary)")
	}
	logF, err := os.OpenFile(i.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(i.Bins.Qdrant, "--config-path", i.confFile())
	cmd.Dir = i.DataDir
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("qdrant start: %w", err)
	}
	i.cmd = cmd
	_ = os.WriteFile(i.pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	go func() {
		_ = cmd.Wait()
		_ = logF.Close()
	}()
	if err := i.WaitReady(45 * time.Second); err != nil {
		logTail, _ := os.ReadFile(i.LogFile)
		_ = i.Stop()
		return fmt.Errorf("%w\nlog:\n%s", err, logTail)
	}
	return nil
}

func (i *Instance) Stop() error {
	if !i.pidAlive() && !i.IsRunning() {
		return nil
	}
	i.signalPID(false)
	if err := i.waitDead(12 * time.Second); err == nil {
		return nil
	}
	i.signalPID(true)
	if err := i.waitDead(3 * time.Second); err == nil {
		return nil
	}
	return fmt.Errorf("qdrant on port %d still running after stop", i.Port)
}

func (i *Instance) signalPID(kill bool) {
	b, err := os.ReadFile(i.pidFile())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	args := []string{strconv.Itoa(pid)}
	if kill {
		args = []string{"-9", strconv.Itoa(pid)}
	}
	_ = exec.Command("kill", args...).Run()
}

func (i *Instance) waitDead(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !i.pidAlive() && (i.Port <= 0 || !postgres.PortListening(i.Port)) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !i.pidAlive() && (i.Port <= 0 || !postgres.PortListening(i.Port)) {
		return nil
	}
	return fmt.Errorf("still running")
}

func (i *Instance) pidAlive() bool {
	b, err := os.ReadFile(i.pidFile())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return false
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
		return true
	}
	return false
}

func (i *Instance) IsRunning() bool {
	if i.pidAlive() {
		return true
	}
	if i.Port <= 0 {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(i.Port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (i *Instance) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	cli := i.LocalClient()
	for time.Now().Before(deadline) {
		if i.IsRunning() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := cli.Ready(ctx)
			cancel()
			if err == nil {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("qdrant on port %d not ready after %s", i.Port, timeout)
}

func (i *Instance) LocalClient() *Client {
	scheme := "http"
	insecure := false
	if useTLS() {
		scheme = "https"
		insecure = true
	}
	return &Client{
		BaseURL:  fmt.Sprintf("%s://127.0.0.1:%d", scheme, i.Port),
		APIKey:   i.Password,
		Insecure: insecure,
	}
}

func (i *Instance) EnsureAppRoles() error {
	if i.Password == "" {
		return nil
	}
	if err := i.writeConfig(); err != nil {
		return err
	}
	cli := i.LocalClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.Ready(ctx); err == nil {
		return nil
	}
	// Config change to api_key requires a restart if the process was already up.
	if i.IsRunning() {
		if err := i.Stop(); err != nil {
			return err
		}
		return i.Start()
	}
	return nil
}

func (i *Instance) ConnString() string {
	return FormatConnString(i.Port, i.Password, i.Name, i.Source, i.Owner)
}

func (i *Instance) CollectionCounts(ctx context.Context) (map[string]int64, error) {
	return i.LocalClient().CollectionCounts(ctx)
}

func (i *Instance) WaitPortFree(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !postgres.PortListening(i.Port) && !i.pidAlive() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("qdrant port %d still in use after %s", i.Port, timeout)
}
