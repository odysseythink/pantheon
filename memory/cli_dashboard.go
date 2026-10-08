package memory

// `octop-memory dashboard` — launch the local memory visualization server.
//
// Mirrors src/octop_memory/adapters/cli/dashboard.py. The FastAPI/uvicorn
// ImportError guards are structurally unnecessary in the Go port (the HTTP
// server is native — dashboard.go), so dashboard is always available.

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func cliDashboardCommand() *cliCommand {
	return &cliCommand{
		name: "dashboard",
		help: "Launch the local memory visualization dashboard (Raw Events, Candidates, Atoms, Journal, Episodes).",
		options: []cliOption{
			{name: "db-path", envvar: "OCTOP_MEMORY_DB", help: "SQLite database path (default: fill in interactively in the dashboard)."},
			{name: "namespace", short: "n", envvar: "OCTOP_MEMORY_NAMESPACE", help: "Memory namespace (default: fill in interactively in the dashboard)."},
			{name: "port", isInt: true, envvar: "OCTOP_MEMORY_DASHBOARD_PORT", help: "Listen port (default: auto-detect starting from 7860)."},
			{name: "host", def: "127.0.0.1", help: "Listen address."},
			{name: "no-browser", flag: true, help: "Don't open the browser automatically."},
		},
		run: cliDashboardRun,
	}
}

// cliFindFreePort mirrors _find_free_port: try ports starting from start,
// return the first bindable one.
func cliFindFreePort(start, attempts int) (int, error) {
	for port := start; port < start+attempts; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("Could not find a free port in range %d~%d", start, start+attempts-1)
}

func cliDashboardRun(c *cliContext, opts map[string]any, args []string) error {
	dbPath := cliOptStr(opts, "db-path")
	namespace := cliOptStr(opts, "namespace")
	host := cliOptStr(opts, "host")
	noBrowser := cliOptBool(opts, "no-browser")

	// Auto-detect a free port.
	port := 0
	if p, ok := opts["port"].(int); ok && p > 0 {
		port = p
	} else {
		found, err := cliFindFreePort(7860, 10)
		if err != nil {
			return cliFailf("%s", err)
		}
		port = found
	}

	url := fmt.Sprintf("http://%s:%d", host, port)

	// Build the startup params hint.
	paramsHint := []string{}
	if dbPath != "" {
		paramsHint = append(paramsHint, fmt.Sprintf("db_path=%s", dbPath))
	}
	if namespace != "" {
		paramsHint = append(paramsHint, fmt.Sprintf("namespace=%s", namespace))
	}

	c.echo(strings.Repeat("=", 60))
	c.echo("  octop-memory dashboard")
	c.echo(strings.Repeat("=", 60))
	c.echo(fmt.Sprintf("  URL: %s", url))
	if len(paramsHint) > 0 {
		c.echo(fmt.Sprintf("  Params: %s", strings.Join(paramsHint, ", ")))
	}
	c.echo("  Press Ctrl+C to stop the server")
	c.echo(strings.Repeat("=", 60))

	server := &DashServer{
		DefaultDBPath:    dbPath,
		DefaultNamespace: namespace,
	}

	if !noBrowser {
		// Open the browser after a short delay, waiting for the server to start.
		time.AfterFunc(1200*time.Millisecond, func() { cliOpenBrowser(url) })
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	err := RunDashServe(addr, server)
	if err != nil && err != http.ErrServerClosed {
		return cliFailf("%s", err)
	}
	return nil
}

// cliOpenBrowser is the webbrowser.open equivalent.
func cliOpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
