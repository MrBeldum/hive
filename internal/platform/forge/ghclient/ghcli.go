package ghclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/colonyops/hive/pkg/executil"
)

// Provider is the credential provider name GitHub accounts are stored under.
const Provider = "github"

func CLIToken(ctx context.Context, exec executil.Executor) (string, error) {
	var stdout bytes.Buffer
	if err := exec.RunStream(ctx, &stdout, io.Discard, "gh", "auth", "token"); err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", errors.New("gh auth token: empty output")
	}
	return token, nil
}
