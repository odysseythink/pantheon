package judge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerdictDefaults(t *testing.T) {
	v := &Verdict{Outcome: "unknown"}
	assert.Equal(t, "unknown", v.Outcome)
	assert.Empty(t, v.MemoriesUsed)
	assert.Empty(t, v.SkillsToExtract)
}

func TestSkillDraftFields(t *testing.T) {
	d := SkillDraft{Name: "a", Description: "d", Body: "b"}
	assert.Equal(t, "a", d.Name)
	assert.Equal(t, "d", d.Description)
	assert.Equal(t, "b", d.Body)
}
