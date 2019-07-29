package tcp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/renproject/aw/handshake"

	"github.com/renproject/aw/protocol"
	"github.com/sirupsen/logrus"
)

// ClientOptions define how the Client manages its connections with remote
// servers. It also defines some other simple behaviours, such as logging.
type ClientOptions struct {
	// Logger is used to log information and errors.
	Logger logrus.FieldLogger
	// Timeout after which the Client will stop an attempt to dial a remote
	// server.
	Timeout time.Duration
	// MaxConnections to remote servers that the Client will maintain.
	MaxConnections int
	// SignVerifier is used during the handshaking process
	SignVerifier protocol.SignVerifier
}

// ClientConn is a network connection associated with a mutex. This allows for
// concurrent safe dialing of the network connection.
type ClientConn struct {
	mu   *sync.Mutex
	conn net.Conn
}

// NewClientConn returns an un-dialed connection.
func NewClientConn() *ClientConn {
	return &ClientConn{
		mu:   new(sync.Mutex),
		conn: nil,
	}
}

// ClientConns is an in memory cache of connections to remote servers that is
// safe for concurrent use.
type ClientConns struct {
	options    ClientOptions
	connsMu    *sync.RWMutex
	conns      map[string]*ClientConn
	handShaker handshake.HandShaker
}

// NewClientConns returns an empty ClientConns that will use ClientOptions to
// control how to dials remote servers.
func NewClientConns(options ClientOptions) *ClientConns {
	if options.Logger == nil {
		panic("pre-condition violation: logger is nil")
	}
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Second
	}
	if options.MaxConnections == 256 {
		options.MaxConnections = 256
	}

	return &ClientConns{
		options:    options,
		connsMu:    new(sync.RWMutex),
		conns:      map[string]*ClientConn{},
		handShaker: handshake.NewHandShaker(options.SignVerifier),
	}
}

// Dial a remote server. If a connection to the remote server already exists,
// then that connection is immediately returned. If a connection to the remote
// server does not exist, then one is established.
func (clientConns *ClientConns) Write(ctx context.Context, addr net.Addr, messageOtw protocol.MessageOnTheWire) error {
	// Pre-condition checks
	if addr == nil {
		panic("pre-condition violation: nil net.Addr")
	}
	if addr.Network() != "tcp" {
		panic(fmt.Errorf("pre-condition violation: expected network=tcp, got network=%v", addr.Network()))
	}

	// Check for an existing connection
	clientConns.connsMu.RLock()
	conn := clientConns.conns[addr.String()]
	clientConns.connsMu.RUnlock()
	if conn != nil && conn.conn != nil {
		// Mutex on the conn
		conn.mu.Lock()
		defer conn.mu.Unlock()

		// Write
		conn.conn.SetWriteDeadline(time.Now().Add(clientConns.options.Timeout))
		if err := messageOtw.Message.Write(conn.conn); err != nil {
			conn.conn.Close()
			delete(clientConns.conns, addr.String())
			return err
		}
		return nil
	}

	// Protect the cache from concurrent writes and establish a connection that
	// can be dialed
	conn, err := func() (*ClientConn, error) {
		clientConns.connsMu.Lock()
		defer clientConns.connsMu.Unlock()

		// Double-check the connection, because while waiting to acquire the
		// write lock another goroutine may have already established the
		// connection
		conn = clientConns.conns[addr.String()]
		if conn != nil {
			return conn, nil
		}

		// Return an error if we are already maintaining the maximum number of
		// connections.
		if len(clientConns.conns) >= clientConns.options.MaxConnections {
			return nil, fmt.Errorf("error dialing %v: exceeded max connections", addr.Network())
		}

		clientConns.conns[addr.String()] = NewClientConn()
		return clientConns.conns[addr.String()], nil
	}()
	if err != nil {
		return err
	}
	if conn.conn != nil {
		// Mutex on the conn
		conn.mu.Lock()
		defer conn.mu.Unlock()

		// Write
		conn.conn.SetWriteDeadline(time.Now().Add(clientConns.options.Timeout))
		if err := messageOtw.Message.Write(conn.conn); err != nil {
			conn.conn.Close()
			delete(clientConns.conns, addr.String())
			return err
		}
		return nil
	}

	// A new connection needs to be dialed, so we lock the connection to prevent
	// multiple dials against the same remote server
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// Double-check the connection, because while waiting to acquire the write lock
	// another goroutine may have already dialed the remote server
	if conn.conn != nil {
		// Mutex on the conn
		conn.mu.Lock()
		defer conn.mu.Unlock()

		// Write
		conn.conn.SetWriteDeadline(time.Now().Add(clientConns.options.Timeout))
		if err := messageOtw.Message.Write(conn.conn); err != nil {
			conn.conn.Close()
			delete(clientConns.conns, addr.String())
			return err
		}
		return nil
	}

	// Dial
	conn.conn, err = net.DialTimeout("tcp", addr.String(), clientConns.options.Timeout)
	if err != nil {
		return err
	}

	hsCTX, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Handshake
	if err := clientConns.handShaker.Initiate(hsCTX, conn.conn); err != nil {
		conn.conn.Close()
		delete(clientConns.conns, addr.String())
		return err
	}

	// Write
	conn.conn.SetWriteDeadline(time.Now().Add(clientConns.options.Timeout))
	if err := messageOtw.Message.Write(conn.conn); err != nil {
		return err
	}

	return nil
}

// Close the connection to a remote server.
func (clientConns *ClientConns) Close(addr net.Addr) error {
	// Protect the cache from concurrent writes and delete the connection
	// associated with this address
	conn := func() *ClientConn {
		clientConns.connsMu.Lock()
		defer clientConns.connsMu.Unlock()

		conn := clientConns.conns[addr.String()]
		delete(clientConns.conns, addr.String())

		return conn
	}()
	// If the connection is nil, or has not been dialed, then there is nothing
	// else to do
	if conn == nil || conn.conn == nil {
		return nil
	}

	// Otherwise, protect the connection from concurrent writes and free the
	// underlying network connection that has been dialed
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// Double-check
	if conn == nil || conn.conn == nil {
		return nil
	}
	err := conn.conn.Close()
	conn.conn = nil

	return err
}

type Client struct {
	conns    *ClientConns
	messages protocol.MessageReceiver
}

func NewClient(conns *ClientConns, messages protocol.MessageReceiver) *Client {
	return &Client{
		conns:    conns,
		messages: messages,
	}
}

func (client *Client) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case messageOtw := <-client.messages:
			client.sendMessageOnTheWire(ctx, messageOtw)
		}
	}
}

func (client *Client) sendMessageOnTheWire(ctx context.Context, messageOtw protocol.MessageOnTheWire) {
	err := client.conns.Write(ctx, messageOtw.To, messageOtw)
	if err == nil {
		return
	}
	client.conns.options.Logger.Errorf("error writing to tcp connection to %v: %v", messageOtw.To.String(), err)

	go func() {
		begin := time.Now()
		delay := time.Duration(1000)
		for i := 0; i < 60; i++ {
			// Dial
			client.conns.options.Logger.Warnf("retrying write to tcp connection to %v with delay of %.4f second(s)", messageOtw.To.String(), time.Now().Sub(begin).Seconds())
			err := client.conns.Write(ctx, messageOtw.To, messageOtw)
			if err != nil {
				time.Sleep(delay * time.Millisecond)
				delay = time.Duration(float64(delay) * 1.6)
				if delay > time.Duration(30000) {
					delay = time.Duration(30000)
				}
				continue
			}
			client.conns.options.Logger.Infof("write to tcp connection to %v success after delay of %.4f second(s)", messageOtw.To.String(), time.Now().Sub(begin).Seconds())
			return
		}
	}()
}
