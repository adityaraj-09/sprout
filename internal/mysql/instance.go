package mysql

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/postgres"
)

type Binaries struct {
	Mysqld     string
	Mysql      string
	Mysqldump  string
	Mysqladmin string
}

func LookBinaries() (Binaries, error) {
	need := []string{"mysqld", "mysql", "mysqldump"}
	found := map[string]string{}
	for _, n := range need {
		p, err := exec.LookPath(n)
		if err != nil {
			return Binaries{}, fmt.Errorf("missing %s in PATH (install MySQL server/client)", n)
		}
		found[n] = p
	}
	admin, _ := exec.LookPath("mysqladmin")
	return Binaries{
		Mysqld:     found["mysqld"],
		Mysql:      found["mysql"],
		Mysqldump:  found["mysqldump"],
		Mysqladmin: admin,
	}, nil
}

func FindOnPath() Binaries {
	b, _ := LookBinaries()
	if b.Mysqld == "" {
		b.Mysqld, _ = exec.LookPath("mysqld")
	}
	if b.Mysql == "" {
		b.Mysql, _ = exec.LookPath("mysql")
	}
	if b.Mysqldump == "" {
		b.Mysqldump, _ = exec.LookPath("mysqldump")
	}
	if b.Mysqladmin == "" {
		b.Mysqladmin, _ = exec.LookPath("mysqladmin")
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
}

func (i *Instance) socket() string { return filepath.Join(i.DataDir, "mysql.sock") }
func (i *Instance) pidFile() string { return filepath.Join(i.DataDir, "mysqld.pid") }
func (i *Instance) autoCNF() string { return filepath.Join(i.DataDir, "auto.cnf") }

func HasDataDir(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err == nil {
		return false
	}
	for _, name := range []string{"auto.cnf", "ibdata1", "mysql.ibd"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	st, err := os.Stat(filepath.Join(dir, "mysql"))
	return err == nil && st.IsDir()
}

func (i *Instance) Init() error {
	if err := os.MkdirAll(i.DataDir, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(i.Bins.Mysqld, "--initialize-insecure", "--datadir="+i.DataDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mysqld --initialize-insecure: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return i.writeConfig()
}

func (i *Instance) writeConfig() error {
	if i.LogFile == "" {
		i.LogFile = filepath.Join(i.DataDir, "mysqld.err")
	}
	if err := os.MkdirAll(filepath.Dir(i.LogFile), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`[mysqld]
datadir=%s
port=%d
bind-address=127.0.0.1
socket=%s
pid-file=%s
log-error=%s
mysqlx=0
skip-name-resolve
`, i.DataDir, i.Port, i.socket(), i.pidFile(), i.LogFile)
	return os.WriteFile(filepath.Join(i.DataDir, "my.cnf"), []byte(body), 0o600)
}

func (i *Instance) PrepareClone() error {
	_ = os.Remove(i.pidFile())
	_ = os.Remove(i.socket())
	_ = os.Remove(i.autoCNF())
	return i.writeConfig()
}

func (i *Instance) Start() error {
	if err := i.writeConfig(); err != nil {
		return err
	}
	if i.IsRunning() {
		return nil
	}
	cmd := exec.Command(i.Bins.Mysqld, "--defaults-file="+filepath.Join(i.DataDir, "my.cnf"))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mysqld start: %w", err)
	}
	_ = cmd.Process.Release()
	if err := i.WaitReady(45 * time.Second); err != nil {
		logTail, _ := os.ReadFile(i.LogFile)
		return fmt.Errorf("%w\nlog:\n%s", err, string(logTail))
	}
	return nil
}

func (i *Instance) Stop() error {
	if !i.IsRunning() {
		return nil
	}
	if i.Bins.Mysqladmin != "" {
		cmd := exec.Command(i.Bins.Mysqladmin, "--socket="+i.socket(), "-uroot", "--protocol=SOCKET", "shutdown")
		_ = cmd.Run()
	}
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
	return fmt.Errorf("mysqld on port %d still running after stop", i.Port)
}

func (i *Instance) IsRunning() bool {
	if i.Bins.Mysqladmin != "" {
		cmd := exec.Command(i.Bins.Mysqladmin, "--socket="+i.socket(), "-uroot", "--protocol=SOCKET", "ping")
		if cmd.Run() == nil {
			return true
		}
	}
	if b, err := os.ReadFile(i.pidFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
				return true
			}
		}
	}
	return false
}

func (i *Instance) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if i.IsRunning() {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("mysqld on port %d not ready after %s", i.Port, timeout)
}

func (i *Instance) ExecSQL(sql string) (string, error) {
	cmd := exec.Command(i.Bins.Mysql, "--socket="+i.socket(), "-uroot", "--protocol=SOCKET", "-N", "-e", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (i *Instance) EnsureAppRoles() error {
	user := postgres.DBUser()
	pass := i.Password
	if pass == "" {
		pass = "sprout"
	}
	escUser := strings.ReplaceAll(user, "'", "''")
	escPass := strings.ReplaceAll(pass, "'", "''")
	sql := fmt.Sprintf(`
CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';
CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';
GRANT ALL PRIVILEGES ON *.* TO '%s'@'%%' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON *.* TO '%s'@'localhost' WITH GRANT OPTION;
FLUSH PRIVILEGES;
`, escUser, escPass, escUser, escPass, escUser, escUser)
	_, err := i.ExecSQL(sql)
	return err
}

func (i *Instance) ConnString(db string) string {
	return FormatConnString(i.Port, db, i.Password, i.Name, i.Source, i.Owner)
}

func (i *Instance) FlushForSnapshot() error {
	_, err := i.ExecSQL("FLUSH TABLES WITH READ LOCK")
	if err != nil {
		return err
	}
	_, _ = i.ExecSQL("UNLOCK TABLES")
	return nil
}
