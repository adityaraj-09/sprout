package compute

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/postgres"
)

// Docker runs official postgres image with PGDATA bind-mounted from the clone.
//
// Important: PrepareClone must already have run on DataDir. The image must NOT
// re-initdb; existing PG_VERSION means "just start".
type Docker struct {
	Bins  postgres.Binaries
	Image string
}

func NewDocker(bins postgres.Binaries) (*Docker, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker not in PATH: %w", err)
	}
	image := os.Getenv("SPROUT_PG_IMAGE")
	if image == "" {
		image = "postgres:" + majorFromBins(bins)
	}
	return &Docker{Bins: bins, Image: image}, nil
}

func majorFromBins(bins postgres.Binaries) string {
	cmd := exec.Command(bins.Postgres, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "16"
	}
	// "postgres (PostgreSQL) 16.4 ..."
	fields := strings.Fields(string(out))
	for _, f := range fields {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' {
			major := f
			if i := strings.IndexByte(major, '.'); i > 0 {
				major = major[:i]
			}
			return major
		}
	}
	return "16"
}

func (d *Docker) Name() string { return "docker" }

func (d *Docker) Available() bool {
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

func (d *Docker) containerName(spec Spec) string {
	// docker names: [a-zA-Z0-9][a-zA-Z0-9_.-]*
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, spec.Name)
	return "sprout-" + safe
}

func (d *Docker) Start(ctx context.Context, spec Spec) (Handle, error) {
	if specEngine(spec) == engine.MySQL {
		return Handle{}, fmt.Errorf("mysql requires SPROUT_COMPUTE=local in this version")
	}
	name := d.containerName(spec)
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()

	args := []string{
		"run", "-d",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", spec.Port),
	}
	if postgres.ProxyEnabled() {
		args = append(args, "-p", fmt.Sprintf("%s:%d:5432", postgres.ProxyBackendHost(), spec.Port))
	}
	args = append(args,
		"-v", spec.DataDir+":/var/lib/postgresql/data",
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust",
		d.Image,
	)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Handle{}, fmt.Errorf("docker run: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	id := strings.TrimSpace(string(out))

	h := Handle{Provider: "docker", ContainerID: id, Name: name, Port: spec.Port, DataDir: spec.DataDir}
	if err := d.waitReady(ctx, h, 60*time.Second); err != nil {
		_ = d.Stop(ctx, h)
		return Handle{}, err
	}
	return h, nil
}

func (d *Docker) Stop(ctx context.Context, h Handle) error {
	name := h.Name
	if name == "" {
		name = h.ContainerID
	}
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("docker rm: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *Docker) IsRunning(ctx context.Context, h Handle) (bool, error) {
	name := h.Name
	if name == "" {
		name = h.ContainerID
	}
	cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (d *Docker) waitReady(ctx context.Context, h Handle, timeout time.Duration) error {
	inst := &postgres.Instance{Port: h.Port, Bins: d.Bins}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if inst.IsRunning() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "40", h.Name).CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker logs:\n%s\n", logs)
	return fmt.Errorf("docker postgres on port %d not ready after %s", h.Port, timeout)
}
