// Command webui-dev serves the web board against a mock hub, for working on webui/ without
// a real hub. Usage: go run ./cmd/webui-dev [-addr 127.0.0.1:8788] [-dir webui/static]
// Sign in with the token it prints (or start with -noauth).
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/mraj602/switchyard/webui"
	"github.com/mraj602/switchyard/webui/devserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8788", "listen address")
	dir := flag.String("dir", "", "serve board files from this directory (live editing) instead of the embedded copy")
	noauth := flag.Bool("noauth", false, "skip the login page")
	flag.Parse()

	var static fs.FS = webui.FS()
	if *dir != "" {
		static = os.DirFS(*dir)
	}
	srv := devserver.New(devserver.Options{Static: static, NoAuth: *noauth, Dev: true})
	fmt.Printf("mock hub on http://%s\nlogin token: %s\n", *addr, devserver.DefaultToken)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
