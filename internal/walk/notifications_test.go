package walk

import (
	"context"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

func TestWalkNotificationSummary(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// SNMPv2-MIB defines one notification (coldStart) and is reached
	// only through IF-MIB's import closure — never matched directly.
	v2 := &model.Module{Name: "SNMPv2-MIB", OIDRoot: "1.3.6.1.6.3.1.1.5", ParseStatus: model.ParseStatusClean}
	v2Syms := []model.Symbol{
		{ModuleName: "SNMPv2-MIB", Name: "coldStart", OID: "1.3.6.1.6.3.1.1.5.1",
			Kind: model.KindNotificationType},
	}
	if err := s.ReplaceModule(ctx, v2, v2Syms, nil, nil); err != nil {
		t.Fatalf("seed SNMPv2-MIB: %v", err)
	}

	// IF-MIB defines linkDown + linkUp and imports SNMPv2-MIB.
	ifmib := &model.Module{
		Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.2", ParseStatus: model.ParseStatusClean,
		Imports: []model.Import{{FromModule: "SNMPv2-MIB", Symbol: "coldStart"}},
	}
	ifSyms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "linkDown", OID: "1.3.6.1.6.3.1.1.5.3", Kind: model.KindNotificationType},
		{ModuleName: "IF-MIB", Name: "linkUp", OID: "1.3.6.1.6.3.1.1.5.4", Kind: model.KindNotificationType},
	}
	if err := s.ReplaceModule(ctx, ifmib, ifSyms, nil, nil); err != nil {
		t.Fatalf("seed IF-MIB: %v", err)
	}

	// FOO-MIB is matched by the walk but defines no notifications.
	foo := &model.Module{Name: "FOO-MIB", OIDRoot: "1.3.6.1.4.1.99", ParseStatus: model.ParseStatusClean}
	fooSyms := []model.Symbol{
		{ModuleName: "FOO-MIB", Name: "fooScalar", OID: "1.3.6.1.4.1.99.1", Kind: model.KindScalar},
	}
	if err := s.ReplaceModule(ctx, foo, fooSyms, nil, nil); err != nil {
		t.Fatalf("seed FOO-MIB: %v", err)
	}

	// The walk matched IF-MIB and FOO-MIB directly.
	summary, err := NotificationSummary(ctx, s, []string{"IF-MIB", "FOO-MIB"})
	if err != nil {
		t.Fatalf("NotificationSummary: %v", err)
	}

	byModule := map[string]int{}
	for _, n := range summary {
		byModule[n.Module] = n.Count
	}

	if byModule["IF-MIB"] != 2 {
		t.Errorf("IF-MIB count = %d, want 2", byModule["IF-MIB"])
	}
	// Closure-derived module is included even though it was never matched.
	if byModule["SNMPv2-MIB"] != 1 {
		t.Errorf("SNMPv2-MIB (closure-derived) count = %d, want 1", byModule["SNMPv2-MIB"])
	}
	// A matched module with no notifications is excluded.
	if _, present := byModule["FOO-MIB"]; present {
		t.Errorf("FOO-MIB has no notifications and must be excluded, got %d", byModule["FOO-MIB"])
	}
}
