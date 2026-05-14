package skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/tool"
)

func TestInjectActiveRegistersSkillTool(t *testing.T) {
	sk := NewRegistry()
	sk.Add(&Skill{Name: "tdd", Body: "Write the test first.", Description: "tdd"})
	_ = sk.Activate("tdd")

	reg := tool.NewRegistry()
	InjectActive(reg, sk)

	res, err := reg.Dispatch(context.Background(), "skill_tdd", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(res, "Write the test first") {
		t.Errorf("expected body in result, got %s", res)
	}
}

func TestInjectActiveSkipsInactive(t *testing.T) {
	sk := NewRegistry()
	sk.Add(&Skill{Name: "tdd", Body: "x"})
	reg := tool.NewRegistry()
	InjectActive(reg, sk)
	if len(reg.Definitions(nil)) != 0 {
		t.Error("expected no tools for inactive skills")
	}
}
