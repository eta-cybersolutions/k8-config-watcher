package actions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"config-watcher/internal/config"
)

func TestSignalActionExecute(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "service.pid")
	if err := os.WriteFile(pidFile, []byte("1234\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	act, err := Build(config.ActionConfig{
		Type:    "signal",
		PIDFile: pidFile,
		Signal:  "SIGHUP",
	})
	if err != nil {
		t.Fatalf("Build() failed: %v", err)
	}

	sigAct := act.(*signalAction)
	gotPid := 0
	gotSig := syscall.Signal(0)
	sigAct.kill = func(pid int, sig syscall.Signal) error {
		gotPid = pid
		gotSig = sig
		return nil
	}

	if err := act.Execute(context.Background()); err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if gotPid != 1234 {
		t.Fatalf("unexpected pid: %d", gotPid)
	}
	if gotSig != syscall.SIGHUP {
		t.Fatalf("unexpected signal: %v", gotSig)
	}
}

func TestSignalActionExecuteWithProcessName(t *testing.T) {
	act, err := Build(config.ActionConfig{
		Type:        "signal",
		ProcessName: "offline-handler",
		Signal:      "SIGTERM",
	})
	if err != nil {
		t.Fatalf("Build() failed: %v", err)
	}

	sigAct := act.(*signalAction)
	sigAct.lookupPID = func(processName string) (int, error) {
		if processName != "offline-handler" {
			t.Fatalf("unexpected process name: %s", processName)
		}
		return 987, nil
	}

	gotPid := 0
	gotSig := syscall.Signal(0)
	sigAct.kill = func(pid int, sig syscall.Signal) error {
		gotPid = pid
		gotSig = sig
		return nil
	}

	if err := act.Execute(context.Background()); err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if gotPid != 987 {
		t.Fatalf("unexpected pid: %d", gotPid)
	}
	if gotSig != syscall.SIGTERM {
		t.Fatalf("unexpected signal: %v", gotSig)
	}
}

func TestFindPIDByNameInProc(t *testing.T) {
	procRoot := t.TempDir()
	pidDir := filepath.Join(procRoot, "321")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(pidDir, "comm"), []byte("offline-handler\n"), 0o644); err != nil {
		t.Fatalf("write comm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("/usr/local/bin/offline-handler\x00--flag\x00"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	got, err := findPIDByNameInProc(procRoot, "offline-handler")
	if err != nil {
		t.Fatalf("findPIDByNameInProc() failed: %v", err)
	}
	if got != 321 {
		t.Fatalf("unexpected pid: %d", got)
	}
}

func TestKubernetesRestartBuildPatchRollout(t *testing.T) {
	a := &kubernetesRestartAction{
		workloadType: "rollout",
		workloadName: "offline-handler",
		namespace:    "powermanage",
		now: func() time.Time {
			return time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
		},
	}

	body, endpoint, err := a.buildPatch()
	if err != nil {
		t.Fatalf("buildPatch failed: %v", err)
	}
	if endpoint != "https://kubernetes.default.svc.cluster.local/apis/argoproj.io/v1alpha1/namespaces/powermanage/rollouts/offline-handler" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if !strings.Contains(string(body), "\"restartAt\":\"2026-04-01T10:00:00Z\"") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestKubernetesRestartBuildPatchDeployment(t *testing.T) {
	a := &kubernetesRestartAction{
		workloadType: "deployment",
		workloadName: "offline-handler",
		namespace:    "powermanage",
		now: func() time.Time {
			return time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
		},
	}

	body, endpoint, err := a.buildPatch()
	if err != nil {
		t.Fatalf("buildPatch failed: %v", err)
	}
	if endpoint != "https://kubernetes.default.svc.cluster.local/apis/apps/v1/namespaces/powermanage/deployments/offline-handler" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if !strings.Contains(string(body), "\"kubectl.kubernetes.io/restartedAt\":\"2026-04-01T10:00:00Z\"") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestKubernetesRestartUnsupportedType(t *testing.T) {
	a := &kubernetesRestartAction{
		workloadType: "statefulset",
		workloadName: "offline-handler",
		namespace:    "powermanage",
		client:       &http.Client{},
		now:          time.Now,
	}
	_, _, err := a.buildPatch()
	if err == nil {
		t.Fatalf("expected unsupported type error")
	}
}

func TestKubernetesAPIHostUsesEnv(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.3.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "6443")
	got := kubernetesAPIHost()
	if got != "https://10.3.0.1:6443" {
		t.Fatalf("unexpected api host: %s", got)
	}
}
