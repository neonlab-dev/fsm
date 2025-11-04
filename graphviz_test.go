package fsm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requirePosix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell script relies on POSIX sh")
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
	return path
}

func TestGraphvizRendererRenderPNGFromFSM(t *testing.T) {
	requirePosix(t)

	tmpDir := t.TempDir()
	inputCapture := filepath.Join(tmpDir, "dot_input.dot")
	fakePNG := []byte("fake-png-binary")

	scriptPath := writeScript(t, tmpDir, "dot", "#!/bin/sh\nset -eu\nif [ \"$1\" != \"-Tpng\" ]; then\n  echo \"unexpected args: $@\" >&2\n  exit 1\nfi\nshift\ncat > \"${FSM_DOT_CAPTURE}\"\nprintf '%s' \""+string(fakePNG)+"\"\n")

	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	renderer := GraphvizRenderer{
		DotPath: scriptPath,
		Env:     []string{"FSM_DOT_CAPTURE=" + inputCapture},
	}

	pngBytes, err := RenderFSMPNG(context.Background(), renderer, machine)
	if err != nil {
		t.Fatalf("RenderPNGFromFSM failed: %v", err)
	}
	if !bytes.Equal(pngBytes, fakePNG) {
		t.Fatalf("unexpected png payload: %q", pngBytes)
	}

	gotDOT, err := os.ReadFile(inputCapture)
	if err != nil {
		t.Fatalf("read captured DOT: %v", err)
	}

	var wantDOT bytes.Buffer
	if err := machine.ExportDOT(&wantDOT); err != nil {
		t.Fatalf("ExportDOT failed: %v", err)
	}

	if strings.TrimSpace(wantDOT.String()) != strings.TrimSpace(string(gotDOT)) {
		t.Fatalf("DOT sent to renderer mismatch\nwant:\n%s\ngot:\n%s", wantDOT.String(), string(gotDOT))
	}
}

func TestGraphvizRendererErrorsWhenBinaryMissing(t *testing.T) {
	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	renderer := GraphvizRenderer{
		DotPath: filepath.Join(t.TempDir(), "does-not-exist"),
	}

	_, err := RenderFSMPNG(context.Background(), renderer, machine)
	if err == nil {
		t.Fatalf("expected error when dot binary is missing")
	}
	if !strings.Contains(err.Error(), "dot binary not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderSnapshotPNGHonorArgsAndEnv(t *testing.T) {
	requirePosix(t)

	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "dot_capture.dot")
	fakePNG := []byte("custom-arg-png")

	script := "#!/bin/sh\nset -eu\nif [ \"$1\" != \"-Tpng\" ]; then\n  echo \"missing -Tpng\" >&2\n  exit 1\nfi\nif [ \"$2\" != \"-Gdpi=150\" ]; then\n  echo \"missing dpi arg\" >&2\n  exit 1\nfi\nshift 2\ncat > \"${FSM_DOT_CAPTURE}\"\nprintf '%s' \"" + string(fakePNG) + "\"\n"
	scriptPath := writeScript(t, tmpDir, "dot", script)

	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	renderer := GraphvizRenderer{
		DotPath: scriptPath,
		Args:    []string{"-Gdpi=150"},
		Env:     []string{"FSM_DOT_CAPTURE=" + capture},
	}

	png, err := RenderSnapshotPNG(context.Background(), renderer, machine.SnapshotGraph())
	if err != nil {
		t.Fatalf("RenderSnapshotPNG failed: %v", err)
	}
	if !bytes.Equal(png, fakePNG) {
		t.Fatalf("expected %q, got %q", fakePNG, png)
	}
}

func TestRenderSnapshotPNGUsesPathLookup(t *testing.T) {
	requirePosix(t)

	tmpDir := t.TempDir()
	capture := filepath.Join(tmpDir, "capture.dot")
	fakePNG := []byte("path-lookup")

	writeScript(t, tmpDir, "dot", "#!/bin/sh\nset -eu\ncat > \"${FSM_DOT_CAPTURE}\"\nprintf '%s' \""+string(fakePNG)+"\"\n")

	// Use PATH lookup (renderer.DotPath vazio)
	t.Setenv("PATH", tmpDir+":"+os.Getenv("PATH"))
	t.Setenv("FSM_DOT_CAPTURE", capture)

	renderer := GraphvizRenderer{}

	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	png, err := RenderFSMPNG(context.Background(), renderer, machine)
	if err != nil {
		t.Fatalf("RenderFSMPNG via PATH falhou: %v", err)
	}
	if !bytes.Equal(png, fakePNG) {
		t.Fatalf("png mismatch: %q", png)
	}
}

func TestRenderSnapshotPNGPropagatesStderr(t *testing.T) {
	requirePosix(t)

	tmpDir := t.TempDir()
	scriptPath := writeScript(t, tmpDir, "dot", "#!/bin/sh\necho \"boom\" >&2\nexit 2\n")

	renderer := GraphvizRenderer{DotPath: scriptPath}

	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	_, err := RenderSnapshotPNG(context.Background(), renderer, machine.SnapshotGraph())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected stderr in error, got %v", err)
	}
}

func TestRenderFSMPNGNilFSM(t *testing.T) {
	_, err := RenderFSMPNG[string, string, any](context.Background(), GraphvizRenderer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "nil FSM") {
		t.Fatalf("expected nil FSM error, got %v", err)
	}
}
