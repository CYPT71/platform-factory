package mcp

import (
	"context"
	"github.com/CYPT71/platform-factory/internal/hooks"
	"log"
	"os"
	"time"
)

type toolHookKey struct{}
type toolHookSettings struct {
	config     hooks.Config
	repository string
}

func toolHookContext(ctx context.Context, root string) (context.Context, error) {
	path := os.Getenv(hooks.ConfigEnv)
	if path == "" || os.Getenv(hooks.ActiveEnv) == "1" {
		return ctx, nil
	}
	cfg, err := hooks.Load(path)
	if err != nil {
		return nil, err
	}
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	return context.WithValue(ctx, toolHookKey{}, toolHookSettings{cfg, root}), nil
}
func emitToolHook(ctx context.Context, logger *log.Logger, kind, name string, code int, duration time.Duration) {
	setting, ok := ctx.Value(toolHookKey{}).(toolHookSettings)
	if !ok {
		return
	}
	event := hooks.NewEvent(kind, name, setting.repository, code, duration)
	if err := hooks.Dispatch(ctx, setting.config, event); err != nil {
		logger.Print("tool hook failed; tool result preserved")
	}
}
