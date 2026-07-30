module github.com/dearmachine/device-client-spike

go 1.23.0

require (
	github.com/agentmail-to/agentmail-go v0.16.0
	github.com/mattn/go-sqlite3 v1.14.32
)

require (
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
)

replace github.com/agentmail-to/agentmail-go => ../.state/agentmail-go
