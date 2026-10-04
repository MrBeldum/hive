package prompt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/colonyops/hive/internal/domain/multiplexer"
	"github.com/colonyops/hive/internal/domain/session"
	"github.com/colonyops/hive/internal/domain/terminal"
)

type fakeFinder struct {
	info   *terminal.SessionInfo
	misses int
}

func (f *fakeFinder) RefreshAll(context.Context) {}

func (f *fakeFinder) DiscoverSession(context.Context, string, map[string]string) (*terminal.SessionInfo, terminal.Integration, error) {
	if f.misses > 0 {
		f.misses--
		return nil, nil, nil
	}
	return f.info, nil, nil
}

// fakeAgent renders a Claude Code style prompt and drops the first
// dropEnters Enters, the way a real agent does while it is still rendering.
// fakeAgent renders a Claude Code style prompt and drops the first
// dropEnters Enters, the way a real agent does while it is still rendering.
type fakeAgent struct {
	input      string
	submitted  []string
	dropEnters int
	dialog     bool
	keys       []string
	pasted     bool
}

func (a *fakeAgent) CapturePane(context.Context, multiplexer.Target, multiplexer.CaptureOptions) (string, error) {
	if a.dialog {
		return "Bash command\n\n  rm -rf build\n\nDo you want to proceed?\n❯ 1. Yes\n  2. No, and tell Claude what to do differently\n", nil
	}
	var b strings.Builder
	for _, s := range a.submitted {
		b.WriteString("❯ " + s + "\n\n⏺ done\n\n")
	}
	b.WriteString("──────────────────────────────\n")
	b.WriteString("❯ " + a.input + "\n")
	b.WriteString("──────────────────────────────\n")
	b.WriteString("  repo · feat/x · 117k/1m 12% · model\n")
	return b.String(), nil
}

func (a *fakeAgent) SendLiteral(_ context.Context, _ multiplexer.Target, text string) error {
	a.input += text
	return nil
}

func (a *fakeAgent) Paste(_ context.Context, _ multiplexer.Target, _ []byte, _ multiplexer.PasteOptions) error {
	a.pasted = true
	a.input += "[Pasted text #1 +1 lines]"
	return nil
}

func (a *fakeAgent) SendKey(_ context.Context, _ multiplexer.Target, key multiplexer.NamedKey) error {
	a.keys = append(a.keys, string(key))
	if key != "Enter" {
		return nil
	}
	if a.dialog {
		a.dialog = false
		return nil
	}
	if a.dropEnters > 0 {
		a.dropEnters--
		return nil
	}
	if a.input != "" {
		a.submitted = append(a.submitted, a.input)
		a.input = ""
	}
	return nil
}

func newTestService(agent *fakeAgent, info *terminal.SessionInfo) *Service {
	svc := NewService(finderOf(&fakeFinder{info: info}), agent)
	svc.timing = SubmitTiming{Poll: time.Millisecond, SettleMin: time.Millisecond, SettleMax: 5 * time.Millisecond, Redraw: 3 * time.Millisecond}
	svc.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return svc
}

var claudePane = &terminal.SessionInfo{PaneID: "%3", DetectedTool: "claude"}

func TestServiceSendPressesEnterOnce(t *testing.T) {
	agent := &fakeAgent{}
	svc := newTestService(agent, claudePane)

	peek, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "/research the auth flow", SendOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Enter"}, agent.keys)
	assert.Equal(t, []string{"/research the auth flow"}, agent.submitted)
	assert.Equal(t, "%3", peek.Pane)
	assert.Contains(t, peek.Tail, "⏺ done")
}

func TestServiceSendReportsADroppedEnter(t *testing.T) {
	agent := &fakeAgent{dropEnters: 1}
	svc := newTestService(agent, claudePane)

	peek, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "continue with the plan", SendOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Enter"}, agent.keys, "Send never presses Enter twice; the caller decides")
	assert.Contains(t, peek.Tail, "❯ continue with the plan", "the screen shows the text still in the box")

	_, err = svc.SendKeys(context.Background(), session.Session{Slug: "s"}, []string{"Enter"}, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"continue with the plan"}, agent.submitted)
}

func TestServiceSendTypesIntoADialog(t *testing.T) {
	agent := &fakeAgent{dialog: true}
	svc := newTestService(agent, claudePane)

	_, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "hello", SendOptions{NoSubmit: true})
	require.NoError(t, err, "nothing is refused; the caller reads the screen")
	assert.Equal(t, "hello", agent.input)
}

func TestServiceSendPastesMultiline(t *testing.T) {
	agent := &fakeAgent{}
	svc := newTestService(agent, claudePane)

	_, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "line one\nline two", SendOptions{})
	require.NoError(t, err)
	assert.True(t, agent.pasted)
	assert.Len(t, agent.submitted, 1)
}

func TestServiceSendNoSubmit(t *testing.T) {
	agent := &fakeAgent{}
	svc := newTestService(agent, claudePane)

	_, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "draft", SendOptions{NoSubmit: true})
	require.NoError(t, err)
	assert.Empty(t, agent.keys)
	assert.Equal(t, "draft", agent.input)
}

func TestServiceMissingPane(t *testing.T) {
	svc := newTestService(&fakeAgent{}, nil)

	_, err := svc.Send(context.Background(), session.Session{Slug: "s"}, "hello", SendOptions{})
	require.ErrorIs(t, err, ErrAgentPaneNotFound)

	peek, err := svc.Peek(context.Background(), session.Session{Slug: "s"}, 10)
	require.NoError(t, err)
	assert.Equal(t, AgentStateMissing, peek.State)
}

func TestServiceRetriesDiscovery(t *testing.T) {
	agent := &fakeAgent{}
	svc := newTestService(agent, claudePane)
	svc.panes = finderOf(&fakeFinder{info: claudePane, misses: discoverAttempts - 1})

	_, err := svc.Peek(context.Background(), session.Session{Slug: "s"}, 5)
	require.NoError(t, err)
}

func TestServicePeek(t *testing.T) {
	svc := newTestService(&fakeAgent{input: "pending words"}, claudePane)

	peek, err := svc.Peek(context.Background(), session.Session{Slug: "s"}, 2)
	require.NoError(t, err)
	assert.Equal(t, AgentStateReady, peek.State)
	require.NotNil(t, peek.Context)
	assert.Equal(t, 12, peek.Context.Percent)
	assert.Equal(t, 2, strings.Count(peek.Tail, "\n")+1)
}

func TestServiceSendKeys(t *testing.T) {
	agent := &fakeAgent{dialog: true}
	svc := newTestService(agent, claudePane)

	peek, err := svc.SendKeys(context.Background(), session.Session{Slug: "s"}, []string{"2", "Enter"}, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"2", "Enter"}, agent.keys)
	assert.Equal(t, AgentStateReady, peek.State, "the returned screen is read after the keys")

	_, err = svc.SendKeys(context.Background(), session.Session{Slug: "s"}, nil, 10)
	require.Error(t, err)
	_, err = svc.SendKeys(context.Background(), session.Session{Slug: "s"}, []string{""}, 10)
	require.Error(t, err)
}

func TestParseContextUsage(t *testing.T) {
	tests := []struct {
		screen string
		want   *ContextUsage
	}{
		{"  repo · hay-kot/fix-1/2 · 117k/1m 12% · opus", &ContextUsage{Used: "117k", Total: "1m", Percent: 12}},
		{"  repo · main · 45000/200000 · sonnet", &ContextUsage{Used: "45000", Total: "200000", Percent: 23}},
		{"no status bar here", nil},
		{"  a · feat/one/two · b", nil},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseContextUsage(tt.screen), tt.screen)
	}
}

func finderOf(f *fakeFinder) func() AgentPaneFinder {
	return func() AgentPaneFinder { return f }
}
