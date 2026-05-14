package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// JudgeConfig parameterizes JudgeAll.
type JudgeConfig struct {
	RunDir           string
	RubricProvider   core.LanguageModel
	PairwiseProvider core.LanguageModel
}

const rubricSystemPrompt = `You are evaluating an AI agent's reply. Given the user message, the agent reply, and the memories/skills the agent was given access to, score each dimension 0-10. Return ONLY JSON: {"correctness":N,"memory_relevance":N,"skill_applied":N,"overall":N,"reason":"..."}`

const pairwiseSystemPrompt = `Two agents answered the same user request. Pick the better reply and explain why in one sentence. Return ONLY JSON: {"winner":"a"|"b"|"tie","reason":"..."}`

// JudgeAll runs rubric + pairwise passes and writes rubric.jsonl and pairwise.jsonl.
func JudgeAll(ctx context.Context, cfg JudgeConfig) error {
	presets, err := listPresets(cfg.RunDir)
	if err != nil {
		return err
	}

	records := make(map[string]map[string]*RunRecord) // preset → inputID → record
	for _, p := range presets {
		m, err := loadRecords(filepath.Join(cfg.RunDir, p, "records.jsonl"))
		if err != nil {
			return err
		}
		records[p] = m
	}

	rubF, err := os.Create(filepath.Join(cfg.RunDir, "rubric.jsonl"))
	if err != nil {
		return err
	}
	defer rubF.Close()
	rubEnc := json.NewEncoder(rubF)

	for _, preset := range presets {
		for inputID, rec := range records[preset] {
			if rec.Error != "" {
				continue
			}
			score := scoreRubric(ctx, cfg.RubricProvider, rec)
			score.PresetName = preset
			score.InputID = inputID
			_ = rubEnc.Encode(score)
		}
	}

	if len(presets) < 2 {
		return nil
	}
	sort.Strings(presets)
	a, b := presets[0], presets[1]

	pairF, err := os.Create(filepath.Join(cfg.RunDir, "pairwise.jsonl"))
	if err != nil {
		return err
	}
	defer pairF.Close()
	pairEnc := json.NewEncoder(pairF)

	for inputID, recA := range records[a] {
		recB, ok := records[b][inputID]
		if !ok {
			continue
		}
		verdict := judgePair(ctx, cfg.PairwiseProvider, recA, recB)
		verdict.InputID = inputID
		verdict.PresetA = a
		verdict.PresetB = b
		_ = pairEnc.Encode(verdict)
	}
	return nil
}

func listPresets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func loadRecords(path string) (map[string]*RunRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	out := map[string]*RunRecord{}
	for s.Scan() {
		var rec RunRecord
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			continue
		}
		if rec.InputID != "" {
			out[rec.InputID] = &rec
		}
	}
	return out, s.Err()
}

func scoreRubric(ctx context.Context, p core.LanguageModel, rec *RunRecord) RubricScore {
	if p == nil {
		return RubricScore{Error: "no judge provider"}
	}
	prompt := fmt.Sprintf("Reply to score:\n%s\n\nInjected memories:\n%s\n\nInjected skills:\n%s",
		rec.Reply, renderSnapshots(rec.InjectedMemories), strings.Join(rec.InjectedSkills, ", "))
	resp, err := p.Generate(ctx, &core.Request{
		SystemPrompt: rubricSystemPrompt,
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: prompt}}},
		},
	})
	if err != nil {
		return RubricScore{Error: err.Error()}
	}
	raw := stripFences(resp.Message.Text())
	var out RubricScore
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		out.Error = err.Error()
	}
	return out
}

func judgePair(ctx context.Context, p core.LanguageModel, a, b *RunRecord) PairwiseVerdict {
	if p == nil {
		return PairwiseVerdict{Consensus: "tie", Reason: "no judge provider"}
	}
	ab := pairwiseOnce(ctx, p, a.Reply, b.Reply)
	ba := pairwiseOnce(ctx, p, b.Reply, a.Reply)
	// Consensus: only if the swap agrees.
	// ab winner "a" ↔ ba winner "b"  → preset a wins
	// ab winner "b" ↔ ba winner "a"  → preset b wins
	consensus := "tie"
	if ab.winner == "a" && ba.winner == "b" {
		consensus = "a"
	} else if ab.winner == "b" && ba.winner == "a" {
		consensus = "b"
	}
	return PairwiseVerdict{
		WinnerAB:  ab.winner,
		WinnerBA:  ba.winner,
		Consensus: consensus,
		Reason:    ab.reason,
	}
}

type swapResult struct {
	winner string // "a" | "b" | "tie"
	reason string
}

func pairwiseOnce(ctx context.Context, p core.LanguageModel, first, second string) swapResult {
	prompt := fmt.Sprintf("Reply A:\n%s\n\nReply B:\n%s", first, second)
	resp, err := p.Generate(ctx, &core.Request{
		SystemPrompt: pairwiseSystemPrompt,
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: prompt}}},
		},
	})
	if err != nil {
		return swapResult{winner: "tie", reason: err.Error()}
	}
	raw := stripFences(resp.Message.Text())
	var parsed struct {
		Winner string `json:"winner"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return swapResult{winner: "tie", reason: err.Error()}
	}
	if parsed.Winner != "a" && parsed.Winner != "b" {
		parsed.Winner = "tie"
	}
	return swapResult{winner: parsed.Winner, reason: parsed.Reason}
}

func renderSnapshots(ms []InjectedSnapshot) string {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "- id=%s content=%q\n", m.ID, m.Content)
	}
	return b.String()
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
