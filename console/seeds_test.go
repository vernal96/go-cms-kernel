package console

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/seeds"
)

type recordingSeedManager struct {
	upPlans    []seeds.Plan
	downPlan   *seeds.Plan
	forcedPlan *seeds.Plan
}

func (m *recordingSeedManager) UpAll(
	_ context.Context,
	plans []seeds.Plan,
) error {
	m.upPlans = append([]seeds.Plan(nil), plans...)
	return nil
}

func (m *recordingSeedManager) Down(
	_ context.Context,
	plan seeds.Plan,
	_ int,
) error {
	m.downPlan = &plan
	return nil
}

func (m *recordingSeedManager) Force(
	_ context.Context,
	plan seeds.Plan,
	_ int,
) error {
	m.forcedPlan = &plan
	return nil
}

func (*recordingSeedManager) StatusAll(
	_ context.Context,
	plans []seeds.Plan,
) ([]seeds.Status, error) {
	result := make([]seeds.Status, 0, len(plans))
	for _, plan := range plans {
		result = append(result, seeds.Status{
			Connection: plan.Connection,
			Module:     plan.Module,
			Source:     plan.Source.ID,
			Tags:       plan.Source.Tags,
		})
	}
	return result, nil
}

func consoleSeedPlan(
	connection string,
	module kernel.ModuleCode,
	source string,
	tags ...seeds.Tag,
) seeds.Plan {
	return seeds.Plan{
		Connection: connection,
		Module:     module,
		Source: seeds.Source{
			ID:     source,
			Tags:   tags,
			Schema: string(module),
			FS: fstest.MapFS{
				"000001_test.up.sql":   {},
				"000001_test.down.sql": {},
			},
			Path: ".",
		},
	}
}

func TestParseSeedTagsTrimsAndDeduplicates(t *testing.T) {
	tags, err := parseSeedTags(" dev,prod, dev ", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "dev" || tags[1] != "prod" {
		t.Fatalf("tags = %#v", tags)
	}

	if _, err := parseSeedTags("dev,,prod", true); err == nil {
		t.Fatal("empty tag was accepted")
	}
	if tags, err := parseSeedTags("", false); err != nil || tags != nil {
		t.Fatalf("omitted tags = %#v, %v", tags, err)
	}
}

func TestSelectSeedPlansMatchesAnyTagAndAdditionalFilters(t *testing.T) {
	plans := []seeds.Plan{
		consoleSeedPlan("main", "core", "sites_dev", "dev"),
		consoleSeedPlan("main", "feature", "feature_shared", "dev", "prod"),
		consoleSeedPlan("logs", "feature", "audit_prod", "prod"),
	}

	selected, err := selectSeedPlans(plans, seedSelection{
		tags:    []seeds.Tag{"dev", "prod"},
		hasTags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 3 {
		t.Fatalf("OR selection = %#v", seedPlanNames(selected))
	}

	selected, err = selectSeedPlans(plans, seedSelection{
		connection: "main",
		module:     "feature",
		source:     "feature_shared",
		tags:       []seeds.Tag{"dev"},
		hasTags:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 ||
		selected[0].Source.ID != "feature_shared" {
		t.Fatalf("filtered selection = %#v", seedPlanNames(selected))
	}

	if _, err := selectSeedPlans(plans, seedSelection{
		tags:    []seeds.Tag{"missing"},
		hasTags: true,
	}); err == nil {
		t.Fatal("unknown tag was accepted")
	}
}

func TestSeedsUpRequiresTagsAndRunsEachMatchingPlanOnce(t *testing.T) {
	plans := []seeds.Plan{
		consoleSeedPlan("main", "core", "sites_dev", "dev"),
		consoleSeedPlan("main", "feature", "shared", "dev", "prod"),
		consoleSeedPlan("logs", "feature", "audit_prod", "prod"),
	}
	manager := &recordingSeedManager{}
	command := &seedsCommand{
		plans:   func() []seeds.Plan { return plans },
		manager: manager,
	}
	streams := IO{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	err := command.Run(
		context.Background(),
		[]string{"up"},
		streams,
	)
	if err == nil || !strings.Contains(err.Error(), "requires -tags") {
		t.Fatalf("up without tags error = %v", err)
	}

	err = command.Run(
		context.Background(),
		[]string{"up", "-tags=dev,prod,dev"},
		streams,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.upPlans) != 3 {
		t.Fatalf("up plans = %#v", seedPlanNames(manager.upPlans))
	}

	err = command.Run(
		context.Background(),
		[]string{"up", "-tags=missing"},
		streams,
	)
	if err == nil || !strings.Contains(err.Error(), "no seed plans match") {
		t.Fatalf("unknown tag error = %v", err)
	}
}

func TestSeedsDownRequiresExactSourceAndHonorsTags(t *testing.T) {
	plan := consoleSeedPlan("main", "core", "sites_dev", "dev")
	manager := &recordingSeedManager{}
	command := &seedsCommand{
		plans:   func() []seeds.Plan { return []seeds.Plan{plan} },
		manager: manager,
	}
	streams := IO{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	err := command.Run(
		context.Background(),
		[]string{
			"down",
			"-connection=main",
			"-module=core",
		},
		streams,
	)
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("down without source error = %v", err)
	}

	err = command.Run(
		context.Background(),
		[]string{
			"down",
			"-connection=main",
			"-module=core",
			"-source=sites_dev",
			"-tags=prod",
		},
		streams,
	)
	if err == nil || !strings.Contains(err.Error(), "no seed plans match") {
		t.Fatalf("down tag mismatch error = %v", err)
	}

	err = command.Run(
		context.Background(),
		[]string{
			"down",
			"-connection=main",
			"-module=core",
			"-source=sites_dev",
			"-tags=dev",
		},
		streams,
	)
	if err != nil {
		t.Fatal(err)
	}
	if manager.downPlan == nil ||
		manager.downPlan.Source.ID != "sites_dev" {
		t.Fatalf("down plan = %#v", manager.downPlan)
	}
}

var _ seedManager = (*recordingSeedManager)(nil)
