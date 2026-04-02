package actions

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"config-watcher/internal/config"
)

type Action interface {
	Type() string
	Execute(ctx context.Context) error
}

type signalAction struct {
	pidFile     string
	processName string
	sig         syscall.Signal
	kill        func(pid int, sig syscall.Signal) error
	lookupPID   func(processName string) (int, error)
}

type commandAction struct {
	command []string
}

type kubernetesRestartAction struct {
	workloadType string
	workloadName string
	namespace    string
	client       *http.Client
	now          func() time.Time
}

func Build(cfg config.ActionConfig) (Action, error) {
	switch strings.ToLower(cfg.Type) {
	case "signal":
		sig, err := parseSignal(cfg.Signal)
		if err != nil {
			return nil, err
		}
		return &signalAction{
			pidFile:     cfg.PIDFile,
			processName: cfg.ProcessName,
			sig:         sig,
			kill:        syscall.Kill,
			lookupPID:   findPIDByName,
		}, nil
	case "command":
		return &commandAction{command: cfg.Command}, nil
	case "kubernetes_restart":
		client, err := newKubernetesHTTPClient()
		if err != nil {
			return nil, err
		}
		return &kubernetesRestartAction{
			workloadType: strings.ToLower(cfg.Kubernetes.WorkloadType),
			workloadName: cfg.Kubernetes.WorkloadName,
			namespace:    cfg.Kubernetes.Namespace,
			client:       client,
			now:          time.Now,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported action type %q", cfg.Type)
	}
}

func parseSignal(raw string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "SIGHUP", "HUP":
		return syscall.SIGHUP, nil
	case "SIGTERM", "TERM":
		return syscall.SIGTERM, nil
	default:
		return 0, fmt.Errorf("unsupported signal %q", raw)
	}
}

func (a *signalAction) Type() string { return "signal" }

func (a *signalAction) Execute(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	var (
		pid int
		err error
	)
	if a.pidFile != "" {
		pid, err = readPID(a.pidFile)
	} else {
		pid, err = a.lookupPID(a.processName)
	}
	if err != nil {
		return err
	}
	if err := a.kill(pid, a.sig); err != nil {
		return fmt.Errorf("failed to signal pid %d: %w", pid, err)
	}
	return nil
}

func readPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid in %q: %w", path, err)
	}
	if pid <= 0 {
		return 0, errors.New("pid must be > 0")
	}
	return pid, nil
}

func findPIDByName(processName string) (int, error) {
	return findPIDByNameInProc("/proc", processName)
}

func findPIDByNameInProc(procRoot, processName string) (int, error) {
	target := filepath.Base(strings.TrimSpace(processName))
	if target == "" {
		return 0, errors.New("process name must not be empty")
	}

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, fmt.Errorf("failed to read proc dir: %w", err)
	}

	foundPID := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		commPath := filepath.Join(procRoot, entry.Name(), "comm")
		commBytes, _ := os.ReadFile(commPath)
		if strings.TrimSpace(string(commBytes)) == target {
			if foundPID == 0 || pid < foundPID {
				foundPID = pid
			}
		}

		exePath := filepath.Join(procRoot, entry.Name(), "exe")
		if exeTarget, err := os.Readlink(exePath); err == nil && filepath.Base(exeTarget) == target {
			if foundPID == 0 || pid < foundPID {
				foundPID = pid
			}
		}

		cmdlinePath := filepath.Join(procRoot, entry.Name(), "cmdline")
		cmdlineBytes, _ := os.ReadFile(cmdlinePath)
		if len(cmdlineBytes) == 0 {
			continue
		}
		argv0 := strings.SplitN(string(cmdlineBytes), "\x00", 2)[0]
		if filepath.Base(argv0) == target {
			if foundPID == 0 || pid < foundPID {
				foundPID = pid
			}
		}
	}

	if foundPID != 0 {
		return foundPID, nil
	}
	return 0, fmt.Errorf("process %q not found", processName)
}

func (a *commandAction) Type() string { return "command" }

func (a *commandAction) Execute(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, a.command[0], a.command[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w; output=%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *kubernetesRestartAction) Type() string { return "kubernetes_restart" }

func (a *kubernetesRestartAction) Execute(ctx context.Context) error {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return fmt.Errorf("failed to read service account token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("empty service account token")
	}

	patchBody, endpoint, err := a.buildPatch()
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewBuffer(patchBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/merge-patch+json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("kubernetes restart patch failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (a *kubernetesRestartAction) buildPatch() ([]byte, string, error) {
	ts := a.now().UTC().Format(time.RFC3339)
	apiHost := kubernetesAPIHost()

	switch a.workloadType {
	case "rollout", "rollouts":
		payload := map[string]interface{}{
			"spec": map[string]interface{}{
				"restartAt": ts,
			},
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, "", err
		}
		u := fmt.Sprintf("%s/apis/argoproj.io/v1alpha1/namespaces/%s/rollouts/%s", apiHost, a.namespace, a.workloadName)
		return b, u, nil
	case "deployment", "deployments":
		payload := map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"annotations": map[string]string{
							"kubectl.kubernetes.io/restartedAt": ts,
						},
					},
				},
			},
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, "", err
		}
		u := fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments/%s", apiHost, a.namespace, a.workloadName)
		return b, u, nil
	default:
		return nil, "", fmt.Errorf("unsupported kubernetes workloadType %q", a.workloadType)
	}
}

func kubernetesAPIHost() string {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	if host != "" {
		if port == "" {
			port = "443"
		}
		return fmt.Sprintf("https://%s:%s", host, port)
	}
	return "https://kubernetes.default.svc.cluster.local"
}

func newKubernetesHTTPClient() (*http.Client, error) {
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	caBytes, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Kubernetes CA cert: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, errors.New("failed to parse Kubernetes CA cert")
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}, nil
}

func JitterDuration(minDur, maxDur time.Duration) time.Duration {
	if maxDur <= minDur {
		return minDur
	}
	return minDur + time.Duration(time.Now().UnixNano()%int64(maxDur-minDur+1))
}
