package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"time"

	"golang.org/x/sys/execabs"
)

const (
	sendSocketName   = "goautossh.send.socket"
	listenSocketName = "goautossh.listen.socket"
)

func main() {
	if err := _main(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func _main() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	user, err := user.Current()
	if err != nil {
		return fmt.Errorf("user.Current: %w", err)
	}
	tmpdir, err := os.MkdirTemp("", "")
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	send, listen := filepath.Join(tmpdir, sendSocketName), filepath.Join(tmpdir, listenSocketName)
	remote := fmt.Sprintf("/tmp/%s.%s", user.Uid, "goautossh.remote.socket")

	args := make([]string, 0, len(os.Args))
	args = append(args, "-L", fmt.Sprintf("%s:%s", send, remote))
	args = append(args, "-R", fmt.Sprintf("%s:%s", remote, listen))
	args = append(args, os.Args...)

	l, err := net.Listen("unix", listen)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}

	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "pong") })
	s := http.Server{}
	go s.Serve(l)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return new(net.Dialer).DialContext(ctx, "unix", send)
			},
		},
	}

	for {
		if err := checkHealth(ctx, client); err != nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("ctx.Err: %w", ctx.Err())
			default:
			}
		}
	}
}

func checkHealth(ctx context.Context, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/ping", nil)
	if err != nil {
		return fmt.Errorf("http.NewRequestWithContext: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("client.Do: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func runSSH(ctx context.Context, healthCheck *http.Client, args []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		cmd := execabs.CommandContext(ctx, "ssh", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Run()
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if err := checkHealth(ctx, healthCheck); err != nil {
			return fmt.Errorf("checkHealth: %w", err)
		}
		timer.Reset(1 * time.Second)
	}
}
