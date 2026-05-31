package compression

import (
	"fmt"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestDebugDetermineBoundaries(t *testing.T) {
	aux := &mockModel{}
	cfg := CompressionConfig{Enabled: true, ProtectLast: 2}
	c := NewDefaultCompressor(cfg, aux)

	msgs := make([]core.Message, 6)
	for i := 0; i < 6; i++ {
		role := core.MESSAGE_ROLE_USER
		if i%2 == 1 {
			role = core.MESSAGE_ROLE_ASSISTANT
		}
		msgs[i] = core.Message{
			Role:    role,
			Content: core.NewTextContent(fmt.Sprintf("msg-%d", i)),
		}
	}

	b := c.determineBoundaries(msgs)
	fmt.Printf("headEnd=%d tailStart=%d\n", b.headEnd, b.tailStart)
	fmt.Printf("head=%v\n", msgs[:b.headEnd])
	fmt.Printf("middle=%v\n", msgs[b.headEnd:b.tailStart])
	fmt.Printf("tail=%v\n", msgs[b.tailStart:])
}
