package main

import (
	"fmt"
	"os"
	"encoding/json"
	"context"

	"github.com/omnistreams/omnistreams-go"
	"github.com/omnistreams/omnistreams-go/transports"
)

func main() {

	ctx := context.Background()
	transport, err := transports.NewWebSocketClientTransport(ctx, "ws://localhost:3000")
	exitOnError(err)

	isClient := true
	conn := omnistreams.NewConnection(transport, isClient)

	stream, err := conn.OpenStream()
	exitOnError(err)


	n, err := stream.Write([]byte("Hi there"))
	exitOnError(err)

	fmt.Println("wrote", n)

	buf := make([]byte, 4096)
	n, err = stream.Read(buf)
	fmt.Println("read", n)

}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func printJson(data interface{}) {
	d, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(d))
}
