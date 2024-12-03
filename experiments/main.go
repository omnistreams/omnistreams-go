package main

import (
	//"crypto/rand"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/coder/websocket"
	"github.com/gobwas/ws"
	gorillaWs "github.com/gorilla/websocket"
	//"github.com/gobwas/ws/wsutil"
)

var uri string
var port int 
var sendBuf []byte

type test struct {
	mode      string
	transport string
}

func main() {

	modeArg := flag.String("mode", "send", "Mode (send/receive)")
	transportArg := flag.String("transport", "tcp", "Transport")
        uriArg := flag.String("uri", "ws://localhost", "URI")
        portArg := flag.Int("port", 5000, "Port")
	flag.Parse()

        uri = *uriArg
        port = *portArg

	table := map[string]map[string]func(){
		"send": map[string]func(){
			"tcp":       sendTcp,
			"websocket": sendWebsocket,
			"gorilla":   sendGorilla,
			"gobwas":    sendGobwas,
		},
		"receive": map[string]func(){
			"tcp":       receiveTcp,
			"websocket": receiveWebsocket,
			"gorilla":   receiveGorilla,
			"gobwas":    receiveGobwas,
		},
	}

	sendBuf = make([]byte, 1*1024*1024)
	//sendBuf = make([]byte, 512*1024)
	//sendBuf = make([]byte, 64*1024)

	fmt.Println("Running")
	table[*modeArg][*transportArg]()
}

func receiveGobwas() {
	http.ListenAndServe(fmt.Sprintf(":%d", port), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			fmt.Println(err)
			return
		}

		buf := make([]byte, len(sendBuf))

		//file, err := os.OpenFile("gc_out.mp4", os.O_RDWR|os.O_CREATE, 0644)
		//checkErr(err)

		//go func() {
		defer conn.Close()

		for {
			header, err := ws.ReadHeader(conn)
			if err != nil {
				fmt.Println(err)
				break
			}

			//payload := make([]byte, header.Length)
			//_, err = io.ReadFull(conn, payload)
			_, err = io.ReadFull(conn, buf[:header.Length])
			if err != nil {
				fmt.Println(err)
				break
			}

			ws.Cipher(buf[:header.Length], header.Mask, 0)

			//_, err = file.Write(buf[:header.Length])
			//checkErr(err)

			if header.OpCode == ws.OpClose {
				return
			}
		}

		//for {
		//        n := 0
		//	msg, _, err := wsutil.ReadClientData(conn)
		//	if err != nil {
		//                fmt.Println(err)
		//                break;
		//	}

		//        n += len(msg)
		//}
		//}()
	}))
}

func sendGobwas() {

        wsConn, br, _, err := ws.Dial(context.Background(), fmt.Sprintf("%s:%d", uri, port))
	checkErr(err)
	defer wsConn.Close()

	if br != nil {
		checkErr(errors.New("Non nil br"))
	}

	//_, err = rand.Read(sendBuf)

	//file, err := os.OpenFile("gc.mp4", os.O_RDWR, 0644)
	//file, err := os.OpenFile("/dev/zero", os.O_RDWR, 0644)
	//checkErr(err)

	for {
		//n, err := file.Read(sendBuf)
		//checkErr(err)

		//mask := ws.NewMask()

		n := len(sendBuf)

		err := ws.WriteHeader(wsConn, ws.Header{
			Fin:    true,
			Length: int64(n),
			OpCode: ws.OpBinary,
			Masked: true,
			//Mask: mask,
		})
		checkErr(err)

		//ws.Cipher(sendBuf[:n], mask, 0)

		_, err = wsConn.Write(sendBuf[:n])
		checkErr(err)

		//if n != len(sendBuf) {
		//        checkErr(errors.New("mismatched length"))
		//}
	}
}

func receiveGorilla() {

	upgrader := gorillaWs.Upgrader{}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Println(err)
			return
		}
		defer wsConn.Close()

		n := 0
		for {
			_, msg, err := wsConn.ReadMessage()
			if err != nil {
				fmt.Println(err)
				break
			}

			n += len(msg)
		}

		fmt.Println("received", n)
	})

        err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
	checkErr(err)
}

func sendGorilla() {
	wsConn, _, err := gorillaWs.DefaultDialer.Dial(fmt.Sprintf("%s:%d", uri, port), nil)
	checkErr(err)
	defer wsConn.Close()

	for {
		err := wsConn.WriteMessage(gorillaWs.BinaryMessage, sendBuf)
		checkErr(err)
	}
}

func receiveWebsocket() {

	ctx := context.Background()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			fmt.Println(err)
			return
		}

		wsConn.SetReadLimit(int64(len(sendBuf)))

		n := 0
		for {
			_, msg, err := wsConn.Read(ctx)
			if err != nil {
				fmt.Println(err)
				break
			}

			n += len(msg)
		}

		fmt.Println("received", n)
	})

	err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
	checkErr(err)
}

func sendWebsocket() {

	ctx := context.Background()

	wsConn, _, err := websocket.Dial(ctx, fmt.Sprintf("%s:%d", uri, port), nil)
	checkErr(err)

	for {
		err := wsConn.Write(ctx, websocket.MessageBinary, sendBuf)
		checkErr(err)
	}
}

func receiveTcp() {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	checkErr(err)

	fmt.Println("Listening")
	for {
		conn, err := listener.Accept()
		checkErr(err)

		fmt.Println("New connection")

		n, err := io.Copy(io.Discard, conn)
		//n, err := io.Copy(conn, conn)
		checkErr(err)

		fmt.Println("received", n)

		err = conn.Close()
		checkErr(err)
	}
}

func sendTcp() {
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", uri, port))
	checkErr(err)

	buf := make([]byte, 1*1024*1024)
	for {
		_, err := conn.Write(buf)
		checkErr(err)
	}

	//fmt.Println("sent", n)
}

func checkErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
