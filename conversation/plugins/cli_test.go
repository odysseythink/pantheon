package plugins

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/conversation"
	"github.com/odysseythink/pantheon/core"
	"github.com/stretchr/testify/require"
)

func TestCLIPlugin_Name(t *testing.T) {
	p := NewCLI(CLIConfig{})
	require.Equal(t, "cli", p.Name())
}

func TestCLIPlugin_Setup_RealModel(t *testing.T) {
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Output: &out})

	c := conversation.New(conversation.WithMaxRounds(2))
	err := c.Use(plugin)
	require.NoError(t, err)

	model := newRealModel(t)
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Say a brief greeting")
	require.NoError(t, err)

	output := out.String()
	require.Contains(t, output, "starting chat")
	require.Contains(t, output, "assistant (to user):")
	require.Contains(t, output, "chat finished")
}

func TestCLIPlugin_SimulateStream(t *testing.T) {
	var out strings.Builder
	p := &cliPlugin{cfg: CLIConfig{Output: &out}}
	p.simulateStream("hello world")
	require.Contains(t, out.String(), "hello")
	require.Contains(t, out.String(), "world")
}

func TestCLIPlugin_AskForFeedback(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("my feedback\n"))
	var out strings.Builder
	plugin := &cliPlugin{cfg: CLIConfig{Input: input, Output: &out}}
	feedback := plugin.askForFeedback(conversation.Route{From: "alice", To: "bob"})
	require.Equal(t, "my feedback", feedback)
}

func TestCLIPlugin_AskForFeedback_EOF(t *testing.T) {
	input := bufio.NewReader(strings.NewReader(""))
	var out strings.Builder
	plugin := &cliPlugin{cfg: CLIConfig{Input: input, Output: &out}}
	feedback := plugin.askForFeedback(conversation.Route{From: "alice", To: "bob"})
	require.Equal(t, "", feedback)
}

func TestCLIPlugin_Setup_InterruptExit(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("exit\n"))
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Input: input, Output: &out})

	c := conversation.New(conversation.WithMaxRounds(10))
	err := c.Use(plugin)
	require.NoError(t, err)

	model := &mockModel{responses: []string{"INTERRUPT"}}
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Hello")
	require.NoError(t, err)

	output := out.String()
	require.Contains(t, output, "Provide feedback")
	require.Contains(t, output, "Exiting.")
}

func TestCLIPlugin_Setup_InterruptContinue(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("keep going\n"))
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Input: input, Output: &out})

	c := conversation.New(conversation.WithMaxRounds(10))
	err := c.Use(plugin)
	require.NoError(t, err)

	model := &mockModel{responses: []string{"INTERRUPT", "TERMINATE"}}
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Hello")
	require.NoError(t, err)

	output := out.String()
	require.Contains(t, output, "Provide feedback")
	require.NotContains(t, output, "Exiting.")
}

func TestCLIPlugin_Setup_RealModel_Stream(t *testing.T) {
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Output: &out, SimulateStream: true})

	c := conversation.New(conversation.WithMaxRounds(2))
	err := c.Use(plugin)
	require.NoError(t, err)

	model := newRealModel(t)
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Say hi")
	require.NoError(t, err)

	output := out.String()
	require.Contains(t, output, "starting chat")
	require.Contains(t, output, "assistant (to user):")
}

func TestCLIPlugin_Setup_ErrorRetryable(t *testing.T) {
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Output: &out, RetryDelay: 10 * time.Millisecond})

	c := conversation.New()
	err := c.Use(plugin)
	require.NoError(t, err)

	model := &mockModel{err: &core.ProviderError{Message: "rate limited", Status: 429}}
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Hello")
	require.Error(t, err)

	output := out.String()
	require.Contains(t, output, "rate limited")
	require.Contains(t, output, "retrying")
}

func TestCLIPlugin_Setup_ErrorNonRetryable(t *testing.T) {
	var out strings.Builder
	plugin := NewCLI(CLIConfig{Output: &out})

	c := conversation.New()
	err := c.Use(plugin)
	require.NoError(t, err)

	model := &mockModel{err: &core.ProviderError{Message: "bad request", Status: 400}}
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Hello")
	require.Error(t, err)

	output := out.String()
	require.Contains(t, output, "bad request")
	require.NotContains(t, output, "retrying")
}
