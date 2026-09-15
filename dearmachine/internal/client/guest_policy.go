package client

// These functions deliberately accept validated facts, not provider fields.
// verification/guest/run.py verifies these exact bodies with Gobra. Parsing,
// authentication, SQLite and provider semantics remain trusted I/O contracts.

// @ ensures result == (controller && authenticated && machineVisible && guestVisible && distinct && exactInbox)
func guestInvitationEligible(controller, authenticated, machineVisible, guestVisible, distinct, exactInbox bool) (result bool) {
	return controller && authenticated && machineVisible && guestVisible && distinct && exactInbox
}

// @ ensures result == (active && authenticated && exactScope && machineVisible && controllerVisible)
func guestDeliveryEligible(active, authenticated, exactScope, machineVisible, controllerVisible bool) (result bool) {
	return active && authenticated && exactScope && machineVisible && controllerVisible
}

// @ ensures result == (active && exactScope && currentGeneration && authenticated && decided && controllerDecision)
func guestExecutionEligible(active, exactScope, currentGeneration, authenticated, decided, controllerDecision bool) (result bool) {
	return active && exactScope && currentGeneration && authenticated && decided && controllerDecision
}

// @ ensures result == (active && exactScope && visible && currentGeneration)
func guestRecipientEligible(active, exactScope, visible, currentGeneration bool) (result bool) {
	return active && exactScope && visible && currentGeneration
}

// An explicit risk exception is not authenticated identity. Callers validate
// exact scope/generation from local state, never from incoming headers.
// @ ensures result == (authenticated || (accepted && active && exactScope && currentGeneration))
func guestSenderEligible(authenticated, accepted, active, exactScope, currentGeneration bool) (result bool) {
	return authenticated || (accepted && active && exactScope && currentGeneration)
}

// @ ensures result == (authenticatedOwner && active && exactScope && currentGeneration && explicitDecision)
func guestExceptionEligible(authenticatedOwner, active, exactScope, currentGeneration, explicitDecision bool) (result bool) {
	return authenticatedOwner && active && exactScope && currentGeneration && explicitDecision
}
