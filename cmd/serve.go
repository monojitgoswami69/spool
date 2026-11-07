package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/ui"
	"github.com/monojitgoswami69/spool/internal/web"
)

var (
	serveHost   string
	servePort   int
	serveNoOpen bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the local web control plane",
	Args:  cobra.NoArgs,
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveHost, "host", "127.0.0.1", "bind address")
	serveCmd.Flags().IntVar(&servePort, "port", 8420, "listen port")
	serveCmd.Flags().BoolVar(&serveNoOpen, "no-open", false, "do not open the browser")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, err := config.Dir("chats")
	if err != nil {
		return err
	}
	store, err := web.NewChatStore(dir)
	if err != nil {
		return err
	}
	client, _ := llmFor(true)
	backend := &localWebBackend{client: client}
	srv := web.New(backend, store)
	srv.Version = Version

	addr := net.JoinHostPort(serveHost, fmt.Sprintf("%d", servePort))
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	displayHost := serveHost
	if displayHost == "0.0.0.0" || displayHost == "::" {
		displayHost = "localhost"
	}
	url := fmt.Sprintf("http://%s:%d", displayHost, servePort)
	if !flagQuiet {
		fmt.Println(ui.Banner(Version))
		fmt.Println()
		fmt.Println(ui.SuccessLine("control plane at " + url))
		fmt.Println(ui.MutedStyle.Render("  chats: " + dir))
		fmt.Println(ui.MutedStyle.Render("  press Ctrl-C to stop"))
	}
	if !serveNoOpen && !flagQuiet {
		_ = openBrowser(url)
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return nil
}
