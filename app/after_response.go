package app

import (
	"net"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

// responseHooks runs callbacks once a response has left the daemon. fiber
// has no such hook; the connection going idle (response written) or away
// (client gone, Connection: close) is the signal.
type responseHooks struct {
	mu      sync.Mutex
	pending map[net.Conn][]func()
}

// afterResponse runs fn once the response to c has been written, or once its
// connection is closed.
func (a *AppServer) afterResponse(c *fiber.Ctx, fn func()) {
	conn := c.Context().Conn()
	a.hooks.mu.Lock()
	defer a.hooks.mu.Unlock()
	if a.hooks.pending == nil {
		a.hooks.pending = map[net.Conn][]func(){}
	}
	a.hooks.pending[conn] = append(a.hooks.pending[conn], fn)
}

func (a *AppServer) connState(conn net.Conn, state fasthttp.ConnState) {
	switch state {
	case fasthttp.StateIdle, fasthttp.StateClosed, fasthttp.StateHijacked:
	default:
		return
	}
	a.hooks.mu.Lock()
	fns := a.hooks.pending[conn]
	delete(a.hooks.pending, conn)
	a.hooks.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
