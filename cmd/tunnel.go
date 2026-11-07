package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/tunnel"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var tunnelPort int

var tunnelCmd = &cobra.Command{
	Use:   "tunnel [project]",
	Short: "Expose a project port via an ephemeral public URL",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runTunnel,
}

func init() {
	tunnelCmd.Flags().IntVar(&tunnelPort, "port", 0, "local port to expose (defaults to the first web service)")
}

func runTunnel(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	_, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	port := tunnelPort
	if port == 0 {
		for _, svc := range lock.Services {
			if svc.IsWorker {
				continue
			}
			if p, ok := lock.PortMap[svc.Name]; ok && p > 0 {
				port = p
				break
			}
			if svc.HostPort > 0 {
				port = svc.HostPort
				break
			}
		}
	}
	if port == 0 {
		return domain.E(domain.KindValidation, "tunnel", "no port available; pass --port", nil)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf("%s", ui.InfoLine("opening tunnel on localhost:"+itoa(port)))
	t, err := tunnel.Start(ctx, port, tunnel.Options{})
	if err != nil {
		return err
	}
	defer t.Close()
	logf("%s", ui.SuccessLine("public URL: "+t.PublicURL()))
	logf("%s", ui.MutedStyle.Render("press Ctrl-C to close the tunnel"))
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
