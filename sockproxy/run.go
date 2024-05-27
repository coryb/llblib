package sockproxy

import (
	"io"
	"net"

	"braces.dev/errtrace"
)

// Run will start the proxy.  This must be done before using the
// session.Attachable in a buildkit solve request.  To release resources you
// MUST Close the provided listener when done using the proxy.
func Run(l net.Listener, dialer func() (net.Conn, error)) error {
	for {
		proxy, err := l.Accept()
		if err != nil {
			return errtrace.Wrap(err)
		}
		conn, err := dialer()
		if err != nil {
			return errtrace.Wrap(err)
		}

		go func() {
			defer proxy.Close()
			_, _ = io.Copy(conn, proxy)
		}()

		go func() {
			defer conn.Close()
			_, _ = io.Copy(proxy, conn)
		}()
	}
}
