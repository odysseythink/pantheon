package conversation

import (
	"sync"
	"testing"
)

func TestConversation_ConcurrentAccess(t *testing.T) {
	c := New()
	c.RegisterParticipant(&Participant{Name: "a"})
	c.RegisterParticipant(&Participant{Name: "b"})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = c.Chats()
		}()
		go func() {
			defer wg.Done()
			c.RegisterParticipant(&Participant{Name: "p"})
		}()
		go func() {
			defer wg.Done()
			c.OnMessage(func(chat Chat, conv *Conversation) {})
		}()
	}
	wg.Wait()
}
