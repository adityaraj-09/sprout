package mongo

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/postgres"
)

type Binaries struct {
	Mongod       string
	Mongosh      string
	Mongodump    string
	Mongorestore string
	Mongo        string
}

func LookBinaries() (Binaries, error) {
	need := []string{"mongod", "mongodump", "mongorestore"}
	found := map[string]string{}
	for _, n := range need {
		p, err := exec.LookPath(n)
		if err != nil {
			return Binaries{}, fmt.Errorf("missing %s in PATH (install MongoDB server/database tools)", n)
		}
		found[n] = p
	}
	sh, _ := exec.LookPath("mongosh")
	legacy, _ := exec.LookPath("mongo")
	if sh == "" && legacy == "" {
		return Binaries{}, fmt.Errorf("missing mongosh in PATH (install MongoDB shell)")
	}
	return Binaries{
		Mongod: found["mongod"], Mongodump: found["mongodump"], Mongorestore: found["mongorestore"],
		Mongosh: sh, Mongo: legacy,
	}, nil
}

func FindOnPath() Binaries {
	b, _ := LookBinaries()
	if b.Mongod == "" {
		b.Mongod, _ = exec.LookPath("mongod")
	}
	if b.Mongodump == "" {
		b.Mongodump, _ = exec.LookPath("mongodump")
	}
	if b.Mongorestore == "" {
		b.Mongorestore, _ = exec.LookPath("mongorestore")
	}
	if b.Mongosh == "" {
		b.Mongosh, _ = exec.LookPath("mongosh")
	}
	if b.Mongo == "" {
		b.Mongo, _ = exec.LookPath("mongo")
	}
	return b
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
	auth     bool
}

func (i *Instance) pidFile() string  { return filepath.Join(i.DataDir, "mongod.pid") }
func (i *Instance) confFile() string { return filepath.Join(i.DataDir, "mongod.conf") }
func (i *Instance) pemFile() string  { return filepath.Join(i.DataDir, "mongod.pem") }
func (i *Instance) caFile() string   { return filepath.Join(i.DataDir, "mongod.ca.pem") }
func (i *Instance) lockFile() string { return filepath.Join(i.DataDir, "mongod.lock") }

func HasDataDir(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err == nil {
		return false
	}
	for _, name := range []string{"WiredTiger.wt", "storage.bson", "_mdb_catalog.wt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func (i *Instance) Init() error {
	if err := os.MkdirAll(i.DataDir, 0o700); err != nil {
		return err
	}
	return i.writeConfig(false)
}

func (i *Instance) writeConfig(authorization bool) error {
	if i.LogFile == "" {
		i.LogFile = filepath.Join(i.DataDir, "mongod.log")
	}
	if err := os.MkdirAll(filepath.Dir(i.LogFile), 0o755); err != nil {
		return err
	}
	if err := writeTLSPEM(i.pemFile(), i.caFile()); err != nil {
		return err
	}
	auth := "disabled"
	if authorization {
		auth = "enabled"
	}
	i.auth = authorization
	// MongoDB 7+ requires CAFile (SERVER-72839). Quote paths for YAML.
	body := fmt.Sprintf(`storage:
  dbPath: %q
net:
  port: %d
  bindIp: %s
  tls:
    mode: requireTLS
    certificateKeyFile: %q
    CAFile: %q
    allowConnectionsWithoutCertificates: true
systemLog:
  destination: file
  path: %q
  logAppend: true
processManagement:
  fork: true
  pidFilePath: %q
security:
  authorization: %s
`, i.DataDir, i.Port, bindIP(), i.pemFile(), i.caFile(), i.LogFile, i.pidFile(), auth)
	return os.WriteFile(i.confFile(), []byte(body), 0o600)
}

func (i *Instance) PrepareClone() error {
	_ = os.Remove(i.pidFile())
	_ = os.Remove(i.lockFile())
	return i.writeConfig(true)
}

func (i *Instance) Start() error {
	if i.LogFile == "" {
		i.LogFile = filepath.Join(i.DataDir, "mongod.log")
	}
	auth := HasDataDir(i.DataDir)
	if b, err := os.ReadFile(i.confFile()); err == nil && strings.Contains(string(b), "authorization: enabled") {
		auth = true
	}
	if err := i.writeConfig(auth); err != nil {
		return err
	}
	if i.IsRunning() {
		return nil
	}
	if i.Bins.Mongod == "" {
		i.Bins = FindOnPath()
	}
	// fork:true — wait for the parent so config/TLS errors are not swallowed.
	cmd := exec.Command(i.Bins.Mongod, "--config", i.confFile())
	out, err := cmd.CombinedOutput()
	if err != nil {
		logTail, _ := os.ReadFile(i.LogFile)
		return fmt.Errorf("mongod start: %w (%s)\nlog:\n%s", err, strings.TrimSpace(string(out)), logTail)
	}
	if err := i.WaitReady(45 * time.Second); err != nil {
		logTail, _ := os.ReadFile(i.LogFile)
		return fmt.Errorf("%w (%s)\nlog:\n%s", err, strings.TrimSpace(string(out)), logTail)
	}
	return nil
}

func (i *Instance) Stop() error {
	if !i.IsRunning() {
		return nil
	}
	_ = i.eval("db.getSiblingDB('admin').shutdownServer()")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !i.IsRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if b, err := os.ReadFile(i.pidFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			_ = exec.Command("kill", strconv.Itoa(pid)).Run()
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !i.IsRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mongod on port %d still running after stop", i.Port)
}

func (i *Instance) IsRunning() bool {
	if b, err := os.ReadFile(i.pidFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
				return true
			}
		}
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(i.Port)), 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return true
	}
	return false
}

func (i *Instance) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if i.IsRunning() {
			if err := i.ping(); err == nil {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("mongod on port %d not ready after %s", i.Port, timeout)
}

func (i *Instance) ping() error {
	// hello is allowed pre-auth; ping may not be once authorization is on.
	if err := i.eval("db.runCommand({hello:1})"); err == nil {
		return nil
	}
	return i.eval("db.runCommand({ping:1})")
}

func (i *Instance) eval(js string) error {
	shell := i.Bins.Mongosh
	if shell == "" {
		shell = i.Bins.Mongo
	}
	if shell == "" {
		return fmt.Errorf("mongosh not in PATH")
	}
	args := []string{
		"--quiet",
		"--tls", "--tlsAllowInvalidCertificates",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(i.Port),
	}
	if i.auth && i.Password != "" {
		args = append(args, "-u", postgres.DBUser(), "-p", i.Password, "--authenticationDatabase", "admin")
	}
	args = append(args, "--eval", js)
	cmd := exec.Command(shell, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (i *Instance) EnsureAppRoles() error {
	user := postgres.DBUser()
	pass := i.Password
	if pass == "" {
		pass = "sprout"
	}
	escUser := strings.ReplaceAll(user, `\`, `\\`)
	escUser = strings.ReplaceAll(escUser, `'`, `\'`)
	escPass := strings.ReplaceAll(pass, `\`, `\\`)
	escPass = strings.ReplaceAll(escPass, `'`, `\'`)
	js := fmt.Sprintf(`
(function() {
  var admin = db.getSiblingDB('admin');
  var info = admin.runCommand({usersInfo: '%s'});
  if (info.ok && info.users && info.users.length > 0) {
    admin.updateUser('%s', {pwd:'%s', roles:[{role:'root', db:'admin'}]});
  } else {
    admin.createUser({user:'%s', pwd:'%s', roles:[{role:'root', db:'admin'}]});
  }
})();
`, escUser, escUser, escPass, escUser, escPass)
	err := i.eval(js)
	if err != nil && !i.auth && i.Password != "" {
		i.auth = true
		err = i.eval(js)
	}
	if err != nil {
		return err
	}
	if !i.auth {
		if err := i.writeConfig(true); err != nil {
			return err
		}
		if err := i.Stop(); err != nil {
			return err
		}
		return i.Start()
	}
	return nil
}

func (i *Instance) ConnString(db string) string {
	return FormatConnString(i.Port, db, i.Password, i.Name, i.Source, i.Owner)
}

func (i *Instance) LockForSnapshot() error {
	if i.Password != "" {
		i.auth = true
	}
	return i.eval("db.adminCommand({fsync:1, lock:true})")
}

func (i *Instance) UnlockForSnapshot() error {
	if i.Password != "" {
		i.auth = true
	}
	return i.eval("db.adminCommand({fsyncUnlock:1})")
}

func (i *Instance) FlushForSnapshot() error {
	if err := i.LockForSnapshot(); err != nil {
		return err
	}
	return i.UnlockForSnapshot()
}
