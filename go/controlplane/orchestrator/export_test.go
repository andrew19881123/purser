// export_test.go exposes internal fields for white-box testing only.
// It is compiled only when running tests.
package orchestrator

import "google.golang.org/grpc/credentials"

// TransportCredsForTest returns the TransportCredentials stored on the client.
// nil means the client was built in insecure (dev) mode.
func (c *GRPCAgentClient) TransportCredsForTest() credentials.TransportCredentials {
	return c.transportCreds
}
