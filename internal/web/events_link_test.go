/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func renderInfoBar(t *testing.T, hasNotifications bool) string {
	t.Helper()
	mod := &model.Module{Name: "TEST-MIB", Description: "A test module."}
	var buf bytes.Buffer
	if err := moduleInfoBar(mod, false, 0, hasNotifications).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render moduleInfoBar: %v", err)
	}
	return buf.String()
}

// TestModuleInfoBarEventsLink asserts the eventconf export link is
// shown only when the module has notifications.
func TestModuleInfoBarEventsLink(t *testing.T) {
	with := renderInfoBar(t, true)
	if !strings.Contains(with, "/m/TEST-MIB/events.xml") {
		t.Errorf("events.xml link missing when module has notifications:\n%s", with)
	}

	without := renderInfoBar(t, false)
	if strings.Contains(without, "events.xml") {
		t.Errorf("events.xml link present when module has no notifications:\n%s", without)
	}
}
