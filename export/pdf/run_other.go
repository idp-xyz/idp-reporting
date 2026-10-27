//go:build !linux

package pdf

import (
	"context"
	"errors"
	"time"
)

// The sandbox needs Linux user and network namespaces and prlimit; elsewhere New fails.
var errPlatform = errors.New("pdf: rendering needs Linux user and network namespaces")

type fileID struct{}

func identity(string) (fileID, error) { return fileID{}, errPlatform }

func (w *Writer) render(context.Context, string, time.Time) ([]byte, error) {
	return nil, errPlatform
}
