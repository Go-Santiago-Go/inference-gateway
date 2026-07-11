// Command server is the entrypoint for the inference-gateway service.
//
// For now it does nothing but prove the toolchain compiles and runs end to end.
// Phase 1 turns this into an HTTP server with health checks and middleware.
package main

import "fmt"

func main() {
	fmt.Println("inference-gateway: starting")
}
