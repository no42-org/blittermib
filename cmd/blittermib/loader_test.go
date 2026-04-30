package main

import (
	"testing"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/model"
)

func TestRejectReason(t *testing.T) {
	cases := []struct {
		name       string
		result     compile.Result
		wantOK     bool
		wantReason string
	}{
		{
			name:   "nil module",
			result: compile.Result{Module: nil},
			wantOK: false,
		},
		{
			name: "empty module name",
			result: compile.Result{
				Module: &model.Module{Name: ""},
			},
			wantOK: false,
		},
		{
			name: "phantom: zero symbols, zero imports",
			result: compile.Result{
				Module:  &model.Module{Name: "Hello"},
				Symbols: nil,
			},
			wantOK: false,
		},
		{
			name: "macro module: zero symbols, has imports",
			result: compile.Result{
				Module: &model.Module{
					Name: "SNMPv2-CONF",
					Imports: []model.Import{
						{FromModule: "SNMPv2-SMI", Symbol: "MODULE-IDENTITY"},
					},
				},
				Symbols: nil,
			},
			wantOK: true,
		},
		{
			name: "normal module: has symbols and imports",
			result: compile.Result{
				Module: &model.Module{
					Name: "IF-MIB",
					Imports: []model.Import{
						{FromModule: "SNMPv2-SMI", Symbol: "Counter32"},
					},
				},
				Symbols: []model.Symbol{
					{Name: "ifInOctets", Kind: model.KindObjectType},
				},
			},
			wantOK: true,
		},
		{
			name: "symbol-only module: kept (defensive — legitimate parsers may omit imports)",
			result: compile.Result{
				Module: &model.Module{Name: "MINIMAL-MIB"},
				Symbols: []model.Symbol{
					{Name: "foo", Kind: model.KindObjectType},
				},
			},
			wantOK: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := rejectReason(c.result)
			if ok != c.wantOK {
				t.Errorf("ok = %v (reason %q), want %v", ok, reason, c.wantOK)
			}
			if !ok && reason == "" {
				t.Error("rejected but no reason given")
			}
		})
	}
}
