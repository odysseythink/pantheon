package skills

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSkill = `---
name: test-driven-development
description: Use when implementing any feature or bugfix
version: 1.1.0
author: Hermes Agent
license: MIT
commands: ["tdd"]
tools: ["file_read", "shell_execute"]
metadata:
  hermes:
    tags: [testing, tdd, development]
    related_skills: [systematic-debugging]
---

# TDD

Write the test first.`

func TestParseSkillBytes(t *testing.T) {
	s, err := parseSkillBytes("SKILL.md", []byte(sampleSkill))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "test-driven-development" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Version != "1.1.0" {
		t.Errorf("version = %q", s.Version)
	}
	if len(s.Commands) != 1 || s.Commands[0] != "tdd" {
		t.Errorf("commands = %v", s.Commands)
	}
	if len(s.Tools) != 2 {
		t.Errorf("tools = %v", s.Tools)
	}
	if len(s.Tags) != 3 {
		t.Errorf("tags = %v", s.Tags)
	}
	if s.Body == "" {
		t.Error("expected body")
	}
}

func TestParseSkillBytesMissingFrontMatter(t *testing.T) {
	_, err := parseSkillBytes("SKILL.md", []byte("# no front matter"))
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseSkillBytesMissingName(t *testing.T) {
	_, err := parseSkillBytes("SKILL.md", []byte("---\ndescription: hi\n---\nbody"))
	if err == nil {
		t.Error("expected error")
	}
}

func TestLoaderFindsSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "software-development", "tdd")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(sampleSkill), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(dir)
	skills, errs := l.Load()
	if len(errs) != 0 {
		t.Errorf("load errors: %v", errs)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "test-driven-development" {
		t.Errorf("name = %q", skills[0].Name)
	}
}
