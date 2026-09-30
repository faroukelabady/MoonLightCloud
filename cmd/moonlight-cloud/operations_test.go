package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOperationsCLIUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"recipients"},
		{"recipients", "add"},
		{"incidents"},
		{"incidents", "list", "--state", "bogus"},
		{"incidents", "reconcile"},
	} {
		var stdout, stderr bytes.Buffer
		if err := operationsCmd(args, &stdout, &stderr); err == nil {
			t.Fatalf("usage must fail: %v", args)
		}
	}
}

func TestOperationsCLILabelValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := operationsCmd([]string{"recipients", "add", "--label", "bad\nlabel",
		"--provider", "whatsapp-main", "--recipient", "201012345678", "--locale", "ar"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("newline label rejected before any database work")
	}
	err = operationsCmd([]string{"recipients", "add", "--label", "ok",
		"--provider", "whatsapp-main", "--recipient", "201012345678", "--locale", "xx"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("locale outside ar/en rejected")
	}
}

func TestOperationsCLIMasking(t *testing.T) {
	if got := maskRecipient("201012345678"); got != "...5678" {
		t.Fatalf("masked: %q", got)
	}
	if strings.Contains(maskRecipient("201012345678"), "20101234") {
		t.Fatal("prefix must not leak")
	}
}
