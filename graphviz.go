package fsm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GraphvizRenderer encapsula a configuração para chamar o binário `dot`.
type GraphvizRenderer struct {
	// DotPath permite informar o caminho completo do executável `dot`.
	// Se vazio, o renderer busca `dot` no PATH.
	DotPath string
	// Args são acrescentados após `-Tpng` (ex.: `-Gdpi=150`).
	Args []string
	// Env é anexado ao ambiente do processo filho.
	Env []string
}

func (r GraphvizRenderer) resolveDot() (string, error) {
	if r.DotPath != "" {
		path, err := exec.LookPath(r.DotPath)
		if err != nil {
			return "", fmt.Errorf("fsm: dot binary not found: %w", err)
		}
		return path, nil
	}
	path, err := exec.LookPath("dot")
	if err != nil {
		return "", fmt.Errorf("fsm: dot binary not found: %w", err)
	}
	return path, nil
}

// RenderSnapshotPNG executa `dot -Tpng` usando o DOT exportado do snapshot informado.
func RenderSnapshotPNG[StateT, EventT Comparable, CtxT any](ctx context.Context, renderer GraphvizRenderer, snap GraphSnapshot[StateT, EventT, CtxT], opts ...GraphDOTOption[StateT, EventT]) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	dotPath, err := renderer.resolveDot()
	if err != nil {
		return nil, err
	}

	var dot bytes.Buffer
	if err := snap.WriteDOT(&dot, opts...); err != nil {
		return nil, fmt.Errorf("fsm: render DOT: %w", err)
	}

	args := append([]string{"-Tpng"}, renderer.Args...)
	cmd := exec.CommandContext(ctx, dotPath, args...)
	cmd.Stdin = bytes.NewReader(dot.Bytes())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(renderer.Env) > 0 {
		cmd.Env = append(cmd.Env, renderer.Env...)
	}

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("fsm: dot execution failed: %w: %s", err, errMsg)
		}
		return nil, fmt.Errorf("fsm: dot execution failed: %w", err)
	}
	return stdout.Bytes(), nil
}

// RenderFSMPNG facilita gerar o PNG diretamente da FSM.
func RenderFSMPNG[StateT, EventT Comparable, CtxT any](ctx context.Context, renderer GraphvizRenderer, f *FSM[StateT, EventT, CtxT], opts ...GraphDOTOption[StateT, EventT]) ([]byte, error) {
	if f == nil {
		return nil, errors.New("fsm: nil FSM pointer")
	}
	return RenderSnapshotPNG(ctx, renderer, f.SnapshotGraph(), opts...)
}
