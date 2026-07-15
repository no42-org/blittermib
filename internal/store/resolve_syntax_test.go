// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Ronny Trommer <ronny@no42.org>

package store

import (
	"context"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// seedInetAddressType loads a minimal INET-ADDRESS-MIB defining the
// InetAddressType enum TC and a consumer module whose scalar's SYNTAX
// names it via an IMPORTS entry — the shape that exercises cross-module
// TC resolution.
func seedInetAddressType(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	inet := &model.Module{Name: "INET-ADDRESS-MIB", ParseStatus: model.ParseStatusClean}
	inetSyms := []model.Symbol{{
		ModuleName: "INET-ADDRESS-MIB", Name: "InetAddressType",
		Kind:   model.KindTextualConvention,
		Syntax: "Enumeration { unknown(0), ipv4(1), ipv6(2) }",
		Status: model.StatusCurrent,
		EnumValues: []model.EnumValue{
			{Name: "unknown", Number: 0},
			{Name: "ipv4", Number: 1},
			{Name: "ipv6", Number: 2},
		},
	}}
	if err := s.ReplaceModule(ctx, inet, inetSyms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule INET-ADDRESS-MIB: %v", err)
	}

	consumer := &model.Module{
		Name:        "TEST-MIB",
		ParseStatus: model.ParseStatusClean,
		Imports: []model.Import{
			{FromModule: "INET-ADDRESS-MIB", Symbol: "InetAddressType"},
		},
	}
	consumerSyms := []model.Symbol{
		{
			ModuleName: "TEST-MIB", Name: "peerAddrType",
			OID: "1.3.6.1.4.1.99.1", Kind: model.KindScalar,
			Syntax: "InetAddressType",
			Access: model.AccessReadOnly, Status: model.StatusCurrent,
		},
		{
			ModuleName: "TEST-MIB", Name: "peerCount",
			OID: "1.3.6.1.4.1.99.2", Kind: model.KindScalar,
			Syntax: "Counter32",
			Access: model.AccessReadOnly, Status: model.StatusCurrent,
		},
		{
			ModuleName: "TEST-MIB", Name: "peerName",
			OID: "1.3.6.1.4.1.99.3", Kind: model.KindScalar,
			Syntax: "OCTET STRING (SIZE(0..255))",
			Access: model.AccessReadOnly, Status: model.StatusCurrent,
		},
	}
	if err := s.ReplaceModule(ctx, consumer, consumerSyms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule TEST-MIB: %v", err)
	}
}

func TestResolveSyntaxTC_ImportedEnum(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedInetAddressType(t, s)

	sym, err := s.GetSymbol(ctx, "TEST-MIB", "peerAddrType")
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}
	tc, err := s.ResolveSyntaxTC(ctx, sym)
	if err != nil {
		t.Fatalf("ResolveSyntaxTC: %v", err)
	}
	if tc == nil {
		t.Fatal("expected InetAddressType TC, got nil")
	}
	if tc.ModuleName != "INET-ADDRESS-MIB" || tc.Name != "InetAddressType" {
		t.Errorf("resolved to %s::%s, want INET-ADDRESS-MIB::InetAddressType", tc.ModuleName, tc.Name)
	}
	if len(tc.EnumValues) != 3 {
		t.Errorf("enum values = %d, want 3", len(tc.EnumValues))
	}
}

func TestResolveSyntaxTC_BaseAndConstrainedReturnNil(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedInetAddressType(t, s)

	// A base type (Counter32) names no TC; a constrained OCTET STRING
	// carries its own shape — neither should resolve.
	for _, name := range []string{"peerCount", "peerName"} {
		sym, err := s.GetSymbol(ctx, "TEST-MIB", name)
		if err != nil {
			t.Fatalf("GetSymbol %s: %v", name, err)
		}
		tc, err := s.ResolveSyntaxTC(ctx, sym)
		if err != nil {
			t.Fatalf("ResolveSyntaxTC %s: %v", name, err)
		}
		if tc != nil {
			t.Errorf("%s: expected nil, got %s::%s", name, tc.ModuleName, tc.Name)
		}
	}
}

// A TC whose consumer isn't loaded (no import row, no local definition)
// must resolve to nil rather than erroring, so callers degrade to plain
// unlinked syntax text.
func TestResolveSyntaxTC_UnknownTypeReturnsNil(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	sym := &model.Symbol{
		ModuleName: "LONE-MIB", Name: "someObj",
		Syntax: "MysteryType",
	}
	tc, err := s.ResolveSyntaxTC(ctx, sym)
	if err != nil {
		t.Fatalf("ResolveSyntaxTC: %v", err)
	}
	if tc != nil {
		t.Errorf("expected nil for unresolvable type, got %s::%s", tc.ModuleName, tc.Name)
	}
}
