package plan

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
)

// withEffect returns the plan of table schema.table in mode sync with rows to
// delete of the codes, whose deletions reach the steps.
func withEffect(schema, table string, codes []string, steps ...Step) *Plan {
	deletes := make([]Row, len(codes))
	for i, c := range codes {
		deletes[i] = Row{Key: values(c)}
	}
	return &Plan{Tables: []Table{{
		Name: name(schema, table), Mode: config.ModeSync, Key: []string{"code"}, Compare: []string{"name"},
		Diff: TableDiff{Deletes: deletes}, Effect: Effect{Steps: steps},
	}}}
}

// chain returns the steps of issue 58: n rows of app.steps form a chain, each
// after the one before, and the first references the plan to delete.
func chain(n int) []Step {
	steps := []Step{step(1, name("app", "steps"), "steps_plan_code_fkey", name("app", "plans"), "CASCADE", 1, "plan_code")}
	for level := 2; level <= n; level++ {
		steps = append(steps, step(level, name("app", "steps"), "steps_after_id_fkey", name("app", "steps"), "CASCADE", 1, "after_id"))
	}
	return steps
}

// effectCase is the plan of a golden file of the block of the effect of the
// deletions, and the totals of its summary.
type effectCase struct {
	name   string
	plan   *Plan
	totals Totals
}

// effectCases are the plans of the golden files of the block of the effect
// of the deletions whose steps repeat foreign keys (ADR-0050), and the
// totals of their summary, which the steps alone decide.
var effectCases = []effectCase{
	{
		// Issue 58, with a chain of 3 rows.
		name:   "effect_chain_3",
		plan:   withEffect("app", "plans", []string{"legacy"}, chain(3)...),
		totals: Totals{Deleted: 3},
	},
	{
		// Issue 58: 299 levels of one foreign key take one line.
		name:   "effect_chain_300",
		plan:   withEffect("app", "plans", []string{"legacy"}, chain(300)...),
		totals: Totals{Deleted: 300},
	},
	{
		// A tree of categories under two roots to delete, whose rows spread
		// out at each level, and notes that reference the categories of
		// every level: the notes of the first level reference the rows of
		// one line, and those of the levels below the rows of a line of 3
		// levels.
		name: "effect_tree",
		plan: withEffect("m", "root", []string{"old", "older"},
			step(1, name("m", "cat"), "cat_root_fkey", name("m", "root"), "CASCADE", 2, "root"),
			step(2, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 3, "parent"),
			step(2, name("m", "note"), "note_cat_fkey", name("m", "cat"), "CASCADE", 1, "cat"),
			step(3, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 5, "parent"),
			step(3, name("m", "note"), "note_cat_fkey", name("m", "cat"), "CASCADE", 2, "cat"),
			step(4, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 2, "parent"),
			step(4, name("m", "note"), "note_cat_fkey", name("m", "cat"), "CASCADE", 3, "cat"),
			step(5, name("m", "note"), "note_cat_fkey", name("m", "cat"), "CASCADE", 2, "cat"),
		),
		totals: Totals{Deleted: 20},
	},
	{
		// Two foreign keys from m.cat to itself, copy_of and parent, that
		// take turns: a line for each key under the line of the other, and
		// one for parent at the first level.
		name: "effect_two_keys",
		plan: withEffect("m", "root", []string{"old"},
			step(1, name("m", "cat"), "cat_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
			step(2, name("m", "cat"), "cat_copy_of_fkey", name("m", "cat"), "CASCADE", 1, "copy_of"),
			step(2, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
			step(3, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
			step(4, name("m", "cat"), "cat_copy_of_fkey", name("m", "cat"), "CASCADE", 1, "copy_of"),
			step(5, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
		),
		totals: Totals{Deleted: 6},
	},
	{
		// A cascade through m.cat, then SET NULL of rows of m.cat that
		// reference the deleted rows, RESTRICT at two levels, and NO ACTION
		// at one level under the line of the cascade.
		name: "effect_mixed",
		plan: withEffect("m", "root", []string{"old"},
			step(1, name("m", "cat"), "cat_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
			step(2, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
			step(3, name("m", "audit"), "audit_cat_fkey", name("m", "cat"), "RESTRICT", 1, "cat"),
			step(3, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
			step(3, name("m", "cat"), "cat_seen_after_fkey", name("m", "cat"), "SET NULL", 1, "seen_after"),
			step(3, name("m", "log"), "log_cat_fkey", name("m", "cat"), "NO ACTION", 1, "cat"),
			step(4, name("m", "audit"), "audit_cat_fkey", name("m", "cat"), "RESTRICT", 1, "cat"),
			step(4, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
			step(4, name("m", "cat"), "cat_seen_after_fkey", name("m", "cat"), "SET NULL", 1, "seen_after"),
			step(5, name("m", "cat"), "cat_seen_after_fkey", name("m", "cat"), "SET NULL", 1, "seen_after"),
		),
		totals: Totals{Deleted: 4, Changed: 3, Failing: 3},
	},
	{
		// A cycle of two tables, m.a and m.b, that reference each other
		// through keys ON DELETE CASCADE: a line for each key.
		name: "effect_cycle",
		plan: withEffect("m", "root", []string{"old"},
			step(1, name("m", "a"), "a_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
			step(2, name("m", "b"), "b_a_id_fkey", name("m", "a"), "CASCADE", 1, "a_id"),
			step(3, name("m", "a"), "a_b_id_fkey", name("m", "b"), "CASCADE", 1, "b_id"),
			step(4, name("m", "b"), "b_a_id_fkey", name("m", "a"), "CASCADE", 1, "a_id"),
			step(5, name("m", "a"), "a_b_id_fkey", name("m", "b"), "CASCADE", 1, "b_id"),
			step(6, name("m", "b"), "b_a_id_fkey", name("m", "a"), "CASCADE", 1, "a_id"),
		),
		totals: Totals{Deleted: 6},
	},
	{
		// Seven tables, each referencing the one before through a key of
		// its own, and a key from the last to itself: the lines below the
		// fifth level stand at the fifth, and name their levels.
		name: "effect_deep",
		plan: withEffect("m", "root", []string{"old"},
			step(1, name("m", "t1"), "t1_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
			step(2, name("m", "t2"), "t2_t1_fkey", name("m", "t1"), "CASCADE", 1, "t1"),
			step(3, name("m", "t3"), "t3_t2_fkey", name("m", "t2"), "CASCADE", 1, "t2"),
			step(4, name("m", "t4"), "t4_t3_fkey", name("m", "t3"), "CASCADE", 1, "t3"),
			step(5, name("m", "t5"), "t5_t4_fkey", name("m", "t4"), "CASCADE", 1, "t4"),
			step(6, name("m", "t6"), "t6_t5_fkey", name("m", "t5"), "CASCADE", 1, "t5"),
			step(7, name("m", "t7"), "t7_t6_fkey", name("m", "t6"), "SET NULL", 2, "t6"),
			step(7, name("m", "t7"), "t7_t6_fkey2", name("m", "t6"), "CASCADE", 1, "t6"),
			step(8, name("m", "t7"), "t7_parent_fkey", name("m", "t7"), "CASCADE", 1, "parent"),
			step(9, name("m", "t7"), "t7_parent_fkey", name("m", "t7"), "CASCADE", 1, "parent"),
			step(10, name("m", "t7"), "t7_parent_fkey", name("m", "t7"), "CASCADE", 1, "parent"),
		),
		totals: Totals{Deleted: 10, Changed: 2},
	},
	{
		// Steps that repeat a key with what is not known: a key whose rows
		// the role may not read, under a line of several levels, and a key
		// SET NULL whose change an action ON UPDATE follows, at two levels.
		name: "effect_repeated_unknown",
		plan: withEffect("app", "plans", []string{"legacy"},
			step(1, name("app", "steps"), "steps_plan_code_fkey", name("app", "plans"), "CASCADE", 1, "plan_code"),
			step(2, name("app", "steps"), "steps_after_id_fkey", name("app", "steps"), "CASCADE", 1, "after_id"),
			notKnown(step(3, name("app", "secret"), "secret_step_fkey", name("app", "steps"), "CASCADE", 0, "step_id"), NoTableSelect),
			step(3, name("app", "steps"), "steps_after_id_fkey", name("app", "steps"), "CASCADE", 1, "after_id"),
			onUpdate(step(3, name("app", "tags"), "tags_step_fkey", name("app", "steps"), "SET NULL", 2, "step_id"), "labels_tag_fkey", name("app", "labels")),
			onUpdate(step(4, name("app", "tags"), "tags_step_fkey", name("app", "steps"), "SET NULL", 1, "step_id"), "labels_tag_fkey", name("app", "labels")),
		),
		totals: Totals{Deleted: 3, Changed: 3, Unknown: 2},
	},
}

func TestRenderEffectsThatRepeatKeys(t *testing.T) {
	t.Parallel()

	for _, tt := range effectCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			if err := Render(&b, tt.plan); err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			checkGolden(t, tt.name, b.Bytes())
		})
	}
}

// effectCount reads the number of rows, the referencing table, and the
// action of a line of the block.
var effectCount = regexp.MustCompile(`^ *(\d+ rows?|Rows) of (\S+) \(.*?, ON DELETE ([A-Z]+(?: [A-Z]+)?)[,.]`)

// The lines of the block count every row of every step once: the rows of
// each referencing table and action that the lines print add up to those of
// the steps, which the plan printed one line for each before ADR-0050, and
// the totals of the summary, which --max-delete counts with the rows to
// delete, stay those of the steps.
func TestEffectLinesCountEveryRow(t *testing.T) {
	t.Parallel()

	cases := append(slices.Clip(effectCases), effectCase{name: "effect", plan: effectPlan(), totals: Totals{Deleted: 126, Changed: 8, Failing: 15, Unknown: 6}})
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.plan.Totals(); got != tt.totals {
				t.Errorf("Totals() = %+v, want %+v", got, tt.totals)
			}
			for i := range tt.plan.Tables {
				table := &tt.plan.Tables[i]
				want := make(map[string]int)
				for _, s := range table.Effect.Steps {
					want[s.Table.String()+" "+s.OnDelete] += s.Rows
				}
				got := make(map[string]int)
				lines := EffectLines(table)
				for _, line := range lines[min(1, len(lines)):] {
					m := effectCount.FindStringSubmatch(line)
					if m == nil {
						t.Fatalf("line %q does not give its rows", line)
					}
					n := 0
					if m[1] != "Rows" {
						n, _ = strconv.Atoi(strings.Fields(m[1])[0])
					}
					got[m[2]+" "+m[3]] += n
				}
				if !maps.Equal(got, want) {
					t.Errorf("rows by table and action of the lines of %s = %v, want those of the steps %v", table.Name, got, want)
				}
				if depth := maxIndent(lines); depth > 2*MaxEffectDepth {
					t.Errorf("a line of %s stands %d spaces in, more than %d", table.Name, depth, 2*MaxEffectDepth)
				}
			}
		})
	}
}

// maxIndent returns the largest number of spaces before a line.
func maxIndent(lines []string) int {
	n := 0
	for _, l := range lines {
		n = max(n, len(l)-len(strings.TrimLeft(l, " ")))
	}
	return n
}

// The tree of the lines: a step whose key a line above it along its parents
// follows counts on that line, and so do the steps under it, and a key that
// comes again under another line gets a line there.
func TestEffectTree(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		steps []Step
		want  []string
	}{
		{
			name:  "a chain",
			steps: chain(4),
			want:  []string{"1 steps_plan_code_fkey 1", "2 steps_after_id_fkey 2 3 4"},
		},
		{
			name:  "a cycle of two tables",
			steps: effectSteps("effect_cycle"),
			want:  []string{"1 a_root_fkey 1", "2 b_a_id_fkey 2 4 6", "3 a_b_id_fkey 3 5"},
		},
		{
			// The walk meets the notes of the deepest level first, under
			// the last of the levels of cat_parent_fkey, and the line holds
			// its steps in the order of their levels all the same.
			name:  "a key that branches off at each level",
			steps: effectSteps("effect_tree"),
			want:  []string{"1 cat_root_fkey 1", "2 cat_parent_fkey 2 3 4", "3 note_cat_fkey 3 4 5", "2 note_cat_fkey 2"},
		},
		{
			// The walk meets m.note at level 5 before m.zzz at level 3, and
			// the lines under the line of cat_parent_fkey keep the order of
			// their first steps.
			name: "lines under a line of several levels",
			steps: []Step{
				step(1, name("m", "cat"), "cat_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
				step(2, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
				step(3, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
				step(3, name("m", "zzz"), "zzz_cat_fkey", name("m", "cat"), "CASCADE", 1, "cat"),
				step(4, name("m", "cat"), "cat_parent_fkey", name("m", "cat"), "CASCADE", 1, "parent"),
				step(5, name("m", "note"), "note_cat_fkey", name("m", "cat"), "CASCADE", 1, "cat"),
			},
			want: []string{"1 cat_root_fkey 1", "2 cat_parent_fkey 2 3 4", "3 zzz_cat_fkey 3", "3 note_cat_fkey 5"},
		},
		{
			name: "a step without a parent stands at the top, at its level",
			steps: []Step{
				step(1, name("m", "t1"), "t1_root_fkey", name("m", "root"), "CASCADE", 1, "root"),
				step(2, name("m", "t9"), "t9_t8_fkey", name("m", "t8"), "CASCADE", 1, "t8"),
			},
			want: []string{"1 t1_root_fkey 1", "2 t9_t8_fkey 2"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, l := range effectTree(tt.steps) {
				levels := make([]string, len(l.steps))
				for i, s := range l.steps {
					levels[i] = strconv.Itoa(tt.steps[s].Level)
				}
				got = append(got, fmt.Sprintf("%d %s %s", l.depth, tt.steps[l.steps[0]].Constraint, strings.Join(levels, " ")))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("effectTree() = %q, want %q", got, tt.want)
			}
		})
	}
}

// effectSteps returns the steps of the case of effectCases of the name.
func effectSteps(name string) []Step {
	i := slices.IndexFunc(effectCases, func(c effectCase) bool { return c.name == name })
	return effectCases[i].plan.Tables[0].Effect.Steps
}
