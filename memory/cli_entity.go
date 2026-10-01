package memory

// entity CLI command group, ported from adapters/cli/entity_cmd.py (L3
// entity anchors, inspection only).

import (
	"context"
	"fmt"
)

// cliEntityTypeChoices mirrors get_args(EntityType) order.
var cliEntityTypeChoices = []string{"User", "Person", "Project", "Decision", "Task", "Fact"}

func cliEntityDict(e *Entity) []cliPair {
	var lastPromotedAt any
	if e.LastPromotedAt != nil {
		lastPromotedAt = pyISOFormat(*e.LastPromotedAt)
	}
	return ord(
		cliPair{"id", e.ID},
		cliPair{"entity_type", string(e.EntityType)},
		cliPair{"canonical_name", e.CanonicalName},
		cliPair{"aliases", cliStringSlice(e.Aliases)},
		cliPair{"atom_count", e.AtomCount},
		cliPair{"last_promoted_at", lastPromotedAt},
		cliPair{"created_at", pyISOFormat(e.CreatedAt)},
	)
}

func cliEntityGroup() *cliGroup {
	return &cliGroup{
		name: "entity",
		help: "Inspect L3 entities (anchors for atoms).",
		commands: []*cliCommand{
			{
				name: "list",
				help: "List entities (alphabetical by canonical_name).",
				options: []cliOption{
					{name: "type", choices: cliEntityTypeChoices, help: "Filter by entity_type."},
					{name: "limit", def: 100, isInt: true},
				},
				run: cliEntityList,
			},
			{
				name: "show",
				help: "Show entity details + its non-deprecated atoms (full UUID required).",
				args:  []cliArg{{name: "ENTITY_ID"}},
				run:   cliEntityShow,
			},
		},
	}
}

func cliEntityList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	filter := cliOptStr(opts, "type")
	limit := opts["limit"].(int)
	var typePtr *EntityType
	if filter != "" {
		t := EntityType(filter)
		typePtr = &t
	}
	entities, err := m.ListEntities(context.Background(), typePtr, limit)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(entities))
		for _, e := range entities {
			dicts = append(dicts, cliEntityDict(e))
		}
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(entities) == 0 {
		c.echo("No entities found.")
		return nil
	}
	for _, e := range entities {
		c.echo(fmt.Sprintf("%s  %-10s  atoms=%3d  %s",
			cliRunePrefix(e.ID, 8), e.EntityType, e.AtomCount, e.CanonicalName))
	}
	return nil
}

func cliEntityShow(c *cliContext, opts map[string]any, args []string) error {
	entityID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	e, err := m.GetEntity(ctx, entityID)
	if err != nil || e == nil {
		// sys.exit(1) — stderr message only.
		c.echoErr(fmt.Sprintf("Entity %s not found.", entityID))
		return errCLIExit1
	}

	atoms, err := m.ListAtoms(ctx, AtomFilter{EntityID: &entityID, Limit: 200})
	if err != nil {
		return cliFailf("%v", err)
	}
	aliases, err := m.ListAliases(ctx, &entityID, 50)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		atomDicts := make([]any, 0, len(atoms))
		for _, a := range atoms {
			atomDicts = append(atomDicts, ord(
				cliPair{"id", a.ID},
				cliPair{"importance", string(a.Importance)},
				cliPair{"confidence", string(a.Confidence)},
				cliPair{"assertion", a.Assertion},
				cliPair{"deprecated", a.DeprecatedAt != nil},
			))
		}
		aliasDicts := make([]any, 0, len(aliases))
		for _, al := range aliases {
			aliasDicts = append(aliasDicts, ord(
				cliPair{"alias", al.Alias},
				cliPair{"created_by", string(al.CreatedBy)},
			))
		}
		c.echo(pyJSON(ord(
			cliPair{"entity", cliEntityDict(e)},
			cliPair{"atoms", atomDicts},
			cliPair{"aliases", aliasDicts},
		), false, "  "))
		return nil
	}

	lastPromoted := "-"
	if e.LastPromotedAt != nil {
		lastPromoted = pyISOFormat(*e.LastPromotedAt)
	}
	c.echo(fmt.Sprintf("id             : %s", e.ID))
	c.echo(fmt.Sprintf("type           : %s", e.EntityType))
	c.echo(fmt.Sprintf("canonical_name : %s", e.CanonicalName))
	c.echo(fmt.Sprintf("atom_count     : %d", e.AtomCount))
	c.echo(fmt.Sprintf("created_at     : %s", pyISOFormat(e.CreatedAt)))
	c.echo(fmt.Sprintf("last_promoted  : %s", lastPromoted))
	c.echo("")
	c.echo(fmt.Sprintf("aliases (%d):", len(aliases)))
	for _, al := range aliases {
		c.echo(fmt.Sprintf("  - %s (by %s)", pyReprScalar(al.Alias), al.CreatedBy))
	}
	c.echo("")
	c.echo(fmt.Sprintf("atoms (%d):", len(atoms)))
	for _, a := range atoms {
		flag := "     "
		if a.DeprecatedAt != nil {
			flag = " DEPR"
		}
		c.echo(fmt.Sprintf("  - %s%s  [%-6s]  %s",
			cliRunePrefix(a.ID, 8), flag, a.Importance, cliRunePrefix(a.Assertion, 80)))
	}
	return nil
}
