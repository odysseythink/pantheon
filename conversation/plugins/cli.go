package plugins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/odysseythink/pantheon/conversation"
	"github.com/odysseythink/pantheon/core"
)

// Config for the CLI plugin.
type CLIConfig struct {
	SimulateStream bool
	RetryDelay     time.Duration
	Input          *bufio.Reader
	Output         io.Writer
}

// NewCLI creates a CLI plugin.
func NewCLI(cfg CLIConfig) conversation.Plugin {
	if cfg.Input == nil {
		cfg.Input = bufio.NewReader(os.Stdin)
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 60 * time.Second
	}
	return &cliPlugin{cfg: cfg}
}

type cliPlugin struct {
	cfg CLIConfig
}

func (p *cliPlugin) Name() string { return "cli" }

func (p *cliPlugin) Setup(conv *conversation.Conversation) error {
	conv.OnStart(func(chat conversation.Chat, c *conversation.Conversation) {
		fmt.Fprint(p.cfg.Output, "\n🚀 starting chat ...\n")
	})

	conv.OnMessage(func(chat conversation.Chat, c *conversation.Conversation) {
		ref := fmt.Sprintf("✎ %s (to %s):", chat.From, chat.To)
		fmt.Fprintln(p.cfg.Output, ref)
		if p.cfg.SimulateStream {
			p.simulateStream(chat.Content)
		} else {
			fmt.Fprintln(p.cfg.Output, chat.Content)
		}
		fmt.Fprintln(p.cfg.Output)
	})

	conv.OnTerminate(func(node string, c *conversation.Conversation) {
		fmt.Fprintf(p.cfg.Output, "\n🚀 chat finished (terminated by %s)\n", node)
	})

	conv.OnInterrupt(func(route conversation.Route, c *conversation.Conversation) {
		feedback := p.askForFeedback(route)
		if strings.TrimSpace(feedback) == "exit" {
			fmt.Fprintln(p.cfg.Output, "Exiting.")
			return
		}
		go func() {
			_ = c.Continue(context.Background(), feedback)
		}()
	})

	conv.OnError(func(err error, route conversation.Route, c *conversation.Conversation) {
		var perr *core.ProviderError
		if errors.As(err, &perr) && perr.IsRetryable() {
			fmt.Fprintf(p.cfg.Output, "   error: %s (retrying in %v...)\n", perr.Error(), p.cfg.RetryDelay)
			time.AfterFunc(p.cfg.RetryDelay, func() {
				_ = c.Retry(context.Background())
			})
			return
		}
		fmt.Fprintf(p.cfg.Output, "   error: %s\n", err.Error())
	})

	return nil
}

func (p *cliPlugin) simulateStream(content string) {
	words := strings.Split(content, " ")
	for i, word := range words {
		if i > 0 {
			fmt.Fprint(p.cfg.Output, " ")
		}
		fmt.Fprint(p.cfg.Output, word)
		time.Sleep(time.Duration(10+time.Now().UnixNano()%40) * time.Millisecond)
	}
	fmt.Fprintln(p.cfg.Output)
}

func (p *cliPlugin) askForFeedback(route conversation.Route) string {
	fmt.Fprintf(p.cfg.Output, "Provide feedback to %s as %s (or 'exit'): ", route.To, route.From)
	line, err := p.cfg.Input.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}
