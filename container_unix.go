//go:build !windows

package llblib

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"braces.dev/errtrace"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

func handleResize(co *ContainerOptions, fd int) {
	co.Setup = append(co.Setup, func(ctx context.Context) error {
		resize := make(chan gateway.WinSize, 1)
		co.Resize = resize

		ch := make(chan os.Signal, 1)
		ch <- syscall.SIGWINCH // Initial resize.

		var eg errgroup.Group
		eg.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					close(ch)
					return nil
				case <-ch:
					ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
					if err != nil {
						return errtrace.Errorf("failed to get winsize: %w", err)
					}
					resize <- gateway.WinSize{
						Cols: uint32(ws.Col),
						Rows: uint32(ws.Row),
					}
				}
			}
		})
		signal.Notify(ch, syscall.SIGWINCH)
		co.Teardown = append(co.Teardown, func() error {
			signal.Stop(ch)
			return nil
		}, func() error {
			if err := eg.Wait(); err != nil {
				return errtrace.Errorf("SIGWINCH event loop failed: %w", err)
			}
			return nil
		})
		return nil
	})
}
