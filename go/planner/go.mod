module github.com/purser/purser/go/planner

go 1.27.1

require github.com/purser/purser/go/gen v0.0.0

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Local placeholder module — resolved via ../gen (and the go.work workspace).
// No remote is required.
replace github.com/purser/purser/go/gen => ../gen
