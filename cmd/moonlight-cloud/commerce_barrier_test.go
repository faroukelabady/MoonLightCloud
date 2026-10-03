package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommerceBarrierRequiresExplicitBoundedConfirmation(t *testing.T) {
	product := "019c0000-0000-7000-8000-000000000011"
	cases := [][]string{
		{"--provider", "https://secret.example/token", "--product", product},
		{"--provider", "shopify-main", "--product", "secret-value"},
		{"--provider", "shopify-main", "--product", product, "--operation", "secret-value"},
		{"--provider", "shopify-main", "--product", product, "--operation", product, "--resolution", "remote_completed"},
		{"--provider", "shopify-main", "--product", product, "--operation", product, "--resolution", "force", "--confirm-remote-settled"},
	}
	for _, args := range cases {
		var out, diagnostics bytes.Buffer
		err := commerceMutationBarrierCmd(args, &out, &diagnostics, true)
		if err == nil {
			t.Fatal("invalid resolution accepted")
		}
		if strings.Contains(err.Error(), "secret") || out.Len() != 0 {
			t.Fatal("unsafe diagnostic or output", err)
		}
	}
}
