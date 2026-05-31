package compression

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type fakeMemoryProvider struct {
	preCompressCalled   bool
	sessionSwitchCalled bool
}

func (f *fakeMemoryProvider) OnPreCompress(messages []core.Message) ([]core.Message, error) {
	f.preCompressCalled = true
	return messages, nil
}

func (f *fakeMemoryProvider) OnSessionSwitch(newSessionID, parentSessionID string) error {
	f.sessionSwitchCalled = true
	return nil
}

func TestMemoryProviderRegistry_OnPreCompress(t *testing.T) {
	r := NewMemoryProviderRegistry()
	f := &fakeMemoryProvider{}
	r.Register(f)

	msgs := []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("hi")}}
	out, err := r.OnPreCompress(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !f.preCompressCalled {
		t.Fatal("expected OnPreCompress to be called")
	}
	if len(out) != 1 {
		t.Fatal("expected 1 message")
	}
}

func TestMemoryProviderRegistry_OnSessionSwitch(t *testing.T) {
	r := NewMemoryProviderRegistry()
	f := &fakeMemoryProvider{}
	r.Register(f)

	if err := r.OnSessionSwitch("new", "old"); err != nil {
		t.Fatal(err)
	}
	if !f.sessionSwitchCalled {
		t.Fatal("expected OnSessionSwitch to be called")
	}
}
