package main

import (
	"fmt"
	"os"

	"github.com/omnistreams/omnistreams-go"
)

func main() {
	ln, err := omnistreams.Listen()
	checkErr(err)

	for {
		sess, err := ln.Accept()
		checkErr(err)

		stream, err := sess.AcceptStream()
		checkErr(err)

		buf := make([]byte, 4096)

		n, err := stream.Read(buf)
		checkErr(err)

		fmt.Println(n, string(buf[:n]))

		n, err = stream.Write(buf[:n])
		checkErr(err)

		fmt.Println("Sent", n)

		stream.Close()
	}
}

func checkErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}
