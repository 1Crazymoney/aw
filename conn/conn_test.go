package conn_test

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"testing"
	"time"

	"github.com/renproject/aw/conn"
)

func printError(err error) {
	switch err {
	case nil:
		return
	case context.DeadlineExceeded:
		fmt.Println("Error: Deadline Exceeded")
	case context.Canceled:
		fmt.Println("Error: Context Cancelled")
	default:
		fmt.Printf("%v\n", err)
	}
}

func TestDialAndThenListen(t *testing.T) {
	clientDone := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn.Dial(
			ctx,
			"localhost:3333",
			func(c net.Conn) {
				writer := bufio.NewWriter(c)
				writer.WriteString("Hello from client!\n")
				writer.Flush()
			},
			func(err error) { log.Println("dialing:", err) },
			func() func(int) time.Duration {
				return func(attempt int) time.Duration {
					return conn.DefaultTimeout(attempt) / 20
				}
			}(),
		)
		// printError(err)
		<-clientDone
	}()

	<-time.After(500 * time.Millisecond)

	verify := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		conn.Listen(
			ctx,
			"localhost:3333",
			func(conn net.Conn) {
				reader := bufio.NewReader(conn)
				line, _, err := reader.ReadLine()
				if err != nil {
					return
				}
				println(string(line))
				verify <- string(line)
				close(clientDone)
			},
			func(err error) { log.Println("listening:", err) },
			conn.All(conn.Max(2), conn.RateLimit(10, 1, 65535)),
		)
		// printError(err)
	}()
	select {
	case <-ctx.Done():
		t.Fatal("Test timeout")
	case line := <-verify:
		if line == "Hello from client!" {
			return
		}
		t.Fatal("Incorrect message received by server")
	}
}

func TestListenAndThenDial(t *testing.T) {
	clientDone := make(chan struct{})
	verify := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		conn.Listen(
			ctx,
			"localhost:3335",
			func(conn net.Conn) {
				reader := bufio.NewReader(conn)
				line, _, err := reader.ReadLine()
				if err != nil {
					return
				}
				println(string(line))
				verify <- string(line)
				close(clientDone)
			},
			func(err error) { log.Println("listening:", err) },
			conn.All(conn.Max(2), conn.RateLimit(10, 1, 65535)),
		)
		// printError(err)
	}()

	<-time.After(500 * time.Millisecond)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn.Dial(
			ctx,
			"localhost:3335",
			func(c net.Conn) {
				writer := bufio.NewWriter(c)
				writer.WriteString("Hello from client!\n")
				writer.Flush()
			},
			func(err error) { log.Println("dialing:", err) },
			func() func(int) time.Duration {
				return func(attempt int) time.Duration {
					return conn.DefaultTimeout(attempt) / 20
				}
			}(),
		)
		// printError(err)
		<-clientDone
	}()

	select {
	case <-ctx.Done():
		t.Fatal("Test timeout")
	case line := <-verify:
		if line == "Hello from client!" {
			return
		}
		t.Fatal("Incorrect message received by server")
	}
}
