// github-archive-panel is an optional loopback-only archive manager.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"githubarchive/internal/panel"
)

func run(ctx context.Context, configPath, address string) error {
	return runMode(ctx, configPath, address, false, "")
}

func runMode(ctx context.Context, configPath, address string, dockerMode bool, trustedOrigin string) error {
	validate := panel.ValidateListen
	open := panel.Open
	if dockerMode {
		validate, open = panel.ValidateDockerListen, panel.OpenDocker
	}
	if trustedOrigin != "" {
		if !dockerMode {
			return fmt.Errorf("trusted origin requires Docker mode")
		}
		if err := panel.ValidateTrustedOrigin(trustedOrigin); err != nil {
			return err
		}
		open = func(path string) (*panel.Server, error) { return panel.OpenDockerWithOrigin(path, trustedOrigin) }
	}
	if err := validate(address); err != nil {
		return err
	}
	p, err := open(configPath)
	if err != nil {
		return err
	}
	defer p.Close()
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Panel: http://%s\n", ln.Addr().String())
	s := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()
	select {
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(shutdown); err != nil {
			_ = s.Close()
		}
		<-done
		return nil
	}
}
func main() {
	path := flag.String("config", "config.yaml", "configuration file")
	addr := flag.String("listen", "127.0.0.1:8765", "loopback listen address")
	dockerMode := flag.Bool("docker", false, "allow the Docker bridge; publish to loopback only")
	trustedOrigin := flag.String("trusted-origin", "", "exact HTTPS browser origin of an authenticated reverse proxy")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMode(ctx, *path, *addr, *dockerMode, *trustedOrigin); err != nil {
		fmt.Fprintln(os.Stderr, "panel:", err)
		os.Exit(1)
	}
}
