package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PresetRunner executes one input against one preset and returns the record.
// Callers build this closure; typically it wires a fresh agent.Engine per
// call against a temp sqlite file. The Item argument carries optional
// history (replay) or just the user message (synthetic benchmark).
type PresetRunner func(ctx context.Context, item Item) (*RunRecord, error)

// RunConfig parameterizes Run.
type RunConfig struct {
	DatasetPath string
	OutDir      string
	Presets     map[string]PresetRunner
	// LoaderFn is the dataset parser. Nil means "use built-in
	// LoadDataset" (synthetic InputItem JSONL); replay supplies its
	// own loader to read ReplayItem rows.
	LoaderFn func(path string) ([]Item, error)
}

// Run executes every (preset × input) combination, writing
// <OutDir>/<preset>/records.jsonl. Already-written (preset, input_id)
// pairs are skipped to support resume.
func Run(ctx context.Context, cfg RunConfig) error {
	loader := cfg.LoaderFn
	if loader == nil {
		loader = LoadDataset
	}
	items, err := loader(cfg.DatasetPath)
	if err != nil {
		return err
	}
	for presetName, runner := range cfg.Presets {
		presetDir := filepath.Join(cfg.OutDir, presetName)
		if err := os.MkdirAll(presetDir, 0o755); err != nil {
			return fmt.Errorf("benchmark: mkdir preset: %w", err)
		}
		recPath := filepath.Join(presetDir, "records.jsonl")
		done, err := readDoneIDs(recPath)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(recPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("benchmark: open records: %w", err)
		}
		enc := json.NewEncoder(f)
		for _, item := range items {
			if _, ok := done[item.GetID()]; ok {
				continue
			}
			rec, err := runner(ctx, item)
			if err != nil {
				rec = &RunRecord{Error: err.Error()}
			}
			rec.PresetName = presetName
			rec.InputID = item.GetID()
			if err := enc.Encode(rec); err != nil {
				f.Close()
				return fmt.Errorf("benchmark: encode: %w", err)
			}
		}
		f.Close()
	}
	return nil
}

func readDoneIDs(path string) (map[string]struct{}, error) {
	done := map[string]struct{}{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return done, nil
		}
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		var rec RunRecord
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			continue
		}
		if rec.InputID != "" {
			done[rec.InputID] = struct{}{}
		}
	}
	return done, s.Err()
}
