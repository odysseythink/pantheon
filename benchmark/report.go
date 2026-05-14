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
	"time"
)

// RenderConfig parameterizes Render.
type RenderConfig struct {
	RunDir  string
	OutPath string
}

// Render emits report.md and report.json into the run dir.
func Render(_ context.Context, cfg RenderConfig) error {
	rubrics, err := loadRubric(filepath.Join(cfg.RunDir, "rubric.jsonl"))
	if err != nil {
		return err
	}
	pairs, _ := loadPairs(filepath.Join(cfg.RunDir, "pairwise.jsonl"))

	presetAvgs := map[string]map[string]float64{}
	presetCounts := map[string]int{}
	for _, r := range rubrics {
		if _, ok := presetAvgs[r.PresetName]; !ok {
			presetAvgs[r.PresetName] = map[string]float64{"correctness": 0, "memory_relevance": 0, "skill_applied": 0, "overall": 0}
		}
		presetAvgs[r.PresetName]["correctness"] += float64(r.Correctness)
		presetAvgs[r.PresetName]["memory_relevance"] += float64(r.MemoryRelevance)
		presetAvgs[r.PresetName]["skill_applied"] += float64(r.SkillApplied)
		presetAvgs[r.PresetName]["overall"] += float64(r.Overall)
		presetCounts[r.PresetName]++
	}
	for p := range presetAvgs {
		n := float64(presetCounts[p])
		if n > 0 {
			for k := range presetAvgs[p] {
				presetAvgs[p][k] /= n
			}
		}
	}

	winners := map[string]int{"a": 0, "b": 0, "tie": 0}
	for _, v := range pairs {
		winners[v.Consensus]++
	}

	var presets []string
	for p := range presetAvgs {
		presets = append(presets, p)
	}
	sort.Strings(presets)

	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark Report — %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "## Rubric (avg score, 0-10)\n\n")
	b.WriteString("| Preset | Correctness | Memory Rel | Skill Appl | Overall |\n")
	b.WriteString("|--------|-------------|------------|------------|---------|\n")
	for _, p := range presets {
		a := presetAvgs[p]
		fmt.Fprintf(&b, "| %-6s | %-11.1f | %-10.1f | %-10.1f | %-7.1f |\n",
			p, a["correctness"], a["memory_relevance"], a["skill_applied"], a["overall"])
	}
	total := winners["a"] + winners["b"] + winners["tie"]
	fmt.Fprintf(&b, "\n## Pairwise (position-swap consensus)\n\n")
	fmt.Fprintf(&b, "- a wins: %d / %d\n- b wins: %d / %d\n- tie:    %d / %d\n",
		winners["a"], total, winners["b"], total, winners["tie"], total)

	if err := os.WriteFile(cfg.OutPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("benchmark: write report.md: %w", err)
	}

	machineOut := filepath.Join(filepath.Dir(cfg.OutPath), "report.json")
	machineData, _ := json.MarshalIndent(map[string]any{
		"rubric_avg":       presetAvgs,
		"pairwise_winners": winners,
		"rubric_rows":      rubrics,
		"pairwise_rows":    pairs,
	}, "", "  ")
	return os.WriteFile(machineOut, machineData, 0o644)
}

func loadRubric(path string) ([]RubricScore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	var out []RubricScore
	for s.Scan() {
		var r RubricScore
		if err := json.Unmarshal(s.Bytes(), &r); err == nil {
			out = append(out, r)
		}
	}
	return out, s.Err()
}

func loadPairs(path string) ([]PairwiseVerdict, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	var out []PairwiseVerdict
	for s.Scan() {
		var v PairwiseVerdict
		if err := json.Unmarshal(s.Bytes(), &v); err == nil {
			out = append(out, v)
		}
	}
	return out, s.Err()
}
