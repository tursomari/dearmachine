package client

import (
	"strings"
	"testing"
)

func TestConversationReferenceRoundTripAndChecksum(t *testing.T) {
	reference := newConversationReference()
	if !validConversationReference(reference) {
		t.Fatalf("generated reference %q is invalid", reference)
	}
	mutated := reference[:len(reference)-1] + "0"
	if mutated == reference {
		mutated = reference[:len(reference)-1] + "1"
	}
	if validConversationReference(mutated) {
		t.Fatalf("checksum accepted mutated reference %q", mutated)
	}
}

func TestConversationFooterExtractsStableReferenceAndStripsMetadata(t *testing.T) {
	reference := newConversationReference()
	body := "Please continue.\n\n> Earlier answer.\n> --\n> Dear Machine - Ref: " + reference + "\n> Magnifica Humanitas\n"

	clean, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("references = %v, want [%s]", references, reference)
	}
	if clean != "Please continue." {
		t.Fatalf("clean body = %q, want only the new contribution", clean)
	}
	for _, metadata := range []string{"Dear Machine - Ref:", "Magnifica Humanitas", "\n> --"} {
		if strings.Contains(clean, metadata) {
			t.Fatalf("clean body retained %q:\n%s", metadata, clean)
		}
	}
}

func TestConversationFooterPreservesQuotedContentForInvalidReference(t *testing.T) {
	reference := newConversationReference()
	checksum := "0"
	if strings.HasSuffix(reference, checksum) {
		checksum = "1"
	}
	invalid := reference[:len(reference)-1] + checksum
	body := "Please assess this excerpt.\n\n> Keep this quoted answer.\n> --\n" +
		"> Dear Machine - Ref: " + invalid + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 0 {
		t.Fatalf("references = %v, want none", references)
	}
	if !strings.Contains(clean, "Keep this quoted answer.") {
		t.Fatalf("invalid reference removed quoted content:\n%s", clean)
	}
}

func TestConversationFooterPreservesAmbiguousForwardedContent(t *testing.T) {
	first := newConversationReference()
	second := newConversationReference()
	body := "Compare these forwarded answers.\n\n" +
		"> First answer.\n> --\n> Dear Machine - Ref: " + first + "\n> Magnifica Humanitas\n\n" +
		"> Second answer.\n> --\n> Dear Machine - Ref: " + second + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 2 {
		t.Fatalf("references = %v, want both forwarded references", references)
	}
	for _, answer := range []string{"First answer.", "Second answer."} {
		if !strings.Contains(clean, answer) {
			t.Fatalf("ambiguous forward lost %q:\n%s", answer, clean)
		}
	}
}

func TestConversationFooterPreservesQuoteWithoutNewContribution(t *testing.T) {
	reference := newConversationReference()
	body := "> Earlier answer.\n> --\n" +
		"> Dear Machine - Ref: " + reference + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("references = %v, want [%s]", references, reference)
	}
	if !strings.Contains(clean, "Earlier answer.") {
		t.Fatalf("quote without a new contribution was removed:\n%s", clean)
	}
}

func TestConversationFooterDeduplicatesNestedStableReferences(t *testing.T) {
	reference := newConversationReference()
	footer := conversationFooter(reference)
	body := "Latest reply.\n\n> " + strings.ReplaceAll(footer, "\n", "\n> ") +
		"\n>> " + strings.ReplaceAll(footer, "\n", "\n>> ")

	_, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("nested references = %v, want one stable reference", references)
	}
}

func TestPrepareInboundMessageDoesNotChooseBetweenDistinctReferences(t *testing.T) {
	first := newConversationReference()
	second := newConversationReference()
	message, reference := prepareInboundMessage(Message{Body: "Forwarded material.\n\n" + conversationFooter(first) + "\n\n" + conversationFooter(second)})
	if reference != "" {
		t.Fatalf("ambiguous reference = %q, want empty", reference)
	}
	if len(message.ConversationReferences) != 2 {
		t.Fatalf("references = %v, want both retained as evidence", message.ConversationReferences)
	}
	if strings.Contains(message.Body, "Dear Machine - Ref:") ||
		strings.Contains(message.Body, conversationFooterMotto) {
		t.Fatalf("ambiguous footer metadata reached clean body:\n%s", message.Body)
	}
}

func TestFormattedReplyFooterRoundTripsThroughHTMLNormalization(t *testing.T) {
	reference := newConversationReference()
	outbound := appendConversationFooter("Formatted answer.", reference)
	clean, references := stripConversationFooters(htmlToText(replyHTML(outbound)))
	if clean != "Formatted answer." {
		t.Fatalf("HTML-normalized answer = %q", clean)
	}
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("HTML-normalized references = %v, want [%s]", references, reference)
	}
}
