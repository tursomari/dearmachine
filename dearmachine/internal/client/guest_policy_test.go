package client

import "testing"

func TestGuestPolicyGatesIndependently(t *testing.T) {
	for bits := 0; bits < 64; bits++ {
		a, b, c, d, e, f := bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0, bits&16 != 0, bits&32 != 0
		if got := guestInvitationEligible(a, b, c, d, e, f); got != (bits == 63) {
			t.Fatalf("invitation %06b = %v", bits, got)
		}
		if got := guestExecutionEligible(a, b, c, d, e, f); got != (bits == 63) {
			t.Fatalf("execution %06b = %v", bits, got)
		}
	}
}

func TestGuestReplyVisibilityIsSeparateFromInvitation(t *testing.T) {
	for bits := 0; bits < 32; bits++ {
		a, b, c, d, e := bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0, bits&16 != 0
		if got := guestDeliveryEligible(a, b, c, d, e); got != (bits == 31) {
			t.Fatalf("delivery %05b = %v", bits, got)
		}
	}
}

func TestGuestRecipientsRequireAllFourFacts(t *testing.T) {
	for bits := 0; bits < 16; bits++ {
		if got := guestRecipientEligible(bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0); got != (bits == 15) {
			t.Fatalf("recipients %04b = %v", bits, got)
		}
	}
}
