//go:build !linux

package pdf

import (
	"context"
	"errors"
	"os"
	"time"
)

// The sandbox needs Linux user and network namespaces and prlimit; elsewhere New fails.
var errPlatform = errors.New("pdf: rendering needs Linux user and network namespaces")

type fileID struct{ size int64 }

func identity(string) (fileID, error)                                    { return fileID{}, errPlatform }
func openIdentity(*os.File) (fileID, error)                              { return fileID{}, errPlatform }
func checkHost(Config) error                                             { return errPlatform }
func (w *Writer) run(context.Context, string, ...string) ([]byte, error) { return nil, errPlatform }

func (w *Writer) render(context.Context, string, time.Time) ([]byte, error) {
	return nil, errPlatform
}
