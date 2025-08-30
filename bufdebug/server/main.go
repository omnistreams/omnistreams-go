package main

import (
	"fmt"
	"os"
	"net/http"
	"encoding/json"

	"github.com/omnistreams/omnistreams-go"
	"github.com/omnistreams/omnistreams-go/transports"
)

func main() {

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		transport, err := transports.NewWebSocketServerTransport(w, r)
		exitOnError(err)

		isClient := false
		conn := omnistreams.NewConnection(transport, isClient)

		go handleConn(conn)
	})

	fmt.Println("Running")
	err := http.ListenAndServe(":3000", nil)
	exitOnError(err)
}

func handleConn(conn *omnistreams.Connection) {
	for {
		stream, err := conn.AcceptStream()
		if err != nil {
			fmt.Println("AcceptStream error:", err)
			break
		}

		go handleStream(stream)
	}
}

func handleStream(stream *omnistreams.Stream) {
	buf := make([]byte, 4096)
	n, err := stream.Read(buf)
	if err != nil {
		fmt.Println("stream read error:", err)
		return
	}

	fmt.Println("read", n)

	n, err = stream.Write(buf[:n])
	if err != nil {
		fmt.Println("stream write error:", err)
		return
	}

	fmt.Println("wrote", n)
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
