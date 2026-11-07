package cmd

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var (
	openPrint   bool
	openService string
)

var openCmd = &cobra.Command{
	Use:   "open [project]",
	Short: "Open a project's live URL in the browser",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runOpen,
}

func init() {
	openCmd.Flags().BoolVar(&openPrint, "print", false, "print the URL without opening a browser")
	openCmd.Flags().StringVar(&openService, "service", "", "open a specific service")
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	_, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	url, service, ok := chooseURL(lock, openService)
	if !ok {
		return fmt.Errorf("no web endpoint found for %s", lock.Project)
	}
	logf("%s", ui.SuccessLine(service+" → "+url))
	if openPrint {
		return nil
	}
	if err := openBrowser(url); err != nil {
		return fmt.Errorf("could not open browser (%v); URL: %s", err, url)
	}
	return nil
}

func chooseURL(lock *domain.Lock, service string) (url, selected string, ok bool) {
	if service != "" {
		for _, svc := range lock.Services {
			if svc.Name != service {
				continue
			}
			port := lock.PortMap[svc.Name]
			if port == 0 {
				port = svc.HostPort
			}
			if port > 0 {
				return fmt.Sprintf("http://localhost:%d", port), svc.Name, true
			}
		}
		return "", "", false
	}
	for _, svc := range lock.Services {
		if svc.IsWorker {
			continue
		}
		port := lock.PortMap[svc.Name]
		if port == 0 {
			port = svc.HostPort
		}
		if port > 0 {
			return fmt.Sprintf("http://localhost:%d", port), svc.Name, true
		}
	}
	return "", "", false
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
