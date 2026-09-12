package client

// These functions deliberately accept validated facts, not provider fields.
// verification/guest/run.py verifies these exact bodies with Gobra. Parsing,
// authentication, SQLite and provider semantics remain trusted I/O contracts.

// @ ensures result == (controller && authenticated && machineVisible && guestVisible && distinct && exactInbox)
func guestInvitationEligible(controller, authenticated, machineVisible, guestVisible, distinct, exactInbox bool) (result bool) {
	return controller && authenticated && machineVisible && guestVisible && distinct && exactInbox
}

// @ ensures result == (active && exactScope && machineVisible && controllerVisible)
func guestDeliveryEligible(active, exactScope, machineVisible, controllerVisible bool) (result bool) {
	return active && exactScope && machineVisible && controllerVisible
}

// @ ensures result == (active && exactScope && currentGeneration && admitted && decided && controllerDecision)
func guestExecutionEligible(active, exactScope, currentGeneration, admitted, decided, controllerDecision bool) (result bool) {
	return active && exactScope && currentGeneration && admitted && decided && controllerDecision
}
