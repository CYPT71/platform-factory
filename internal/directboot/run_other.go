//go:build !(linux && amd64) && !(darwin && cgo)

package directboot

import (
	"context"
	"errors"
)

func Run(context.Context, Config) (Result, error) {
	return Result{}, errors.New("direct boot: native backend is unavailable on this platform")
}
