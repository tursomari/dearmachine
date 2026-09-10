package main

import (
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

func TestRegistryOutputLabelsSenderAndInboxWithoutChangingRows(t *testing.T) {
	registry := client.PairRegistry{
		Inboxes: []client.Inbox{
			{ID: "inbox-b", Address: "beta@example.test", Transport: "openmail"},
			{ID: "inbox-a", Address: "alpha@example.test", Transport: "agentmail"},
		},
		Pairs: []client.Pair{
			{ID: "pair-a", UserEmail: "gamma@example.test", InboxID: "inbox-a"},
			{ID: "pair-b", UserEmail: "delta@example.test", InboxID: "inbox-b"},
		},
	}
	var output strings.Builder
	if err := printRegistry(&output, registry); err != nil {
		t.Fatal(err)
	}
	want := "Pair UUID\tAuthorized sender\tDear Machine inbox\tTransport\n" +
		"pair-a\tgamma@example.test\talpha@example.test\tagentmail\n" +
		"pair-b\tdelta@example.test\tbeta@example.test\topenmail\n"
	if output.String() != want {
		t.Fatalf("registry output = %q, want %q", output.String(), want)
	}
}

func TestEmptyRegistryOutputHasNoMisleadingPairHeader(t *testing.T) {
	var output strings.Builder
	if err := printRegistry(&output, client.PairRegistry{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "No pairs are registered.\n" {
		t.Fatalf("empty registry output = %q", output.String())
	}
}
