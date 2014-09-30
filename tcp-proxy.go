package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/mgutz/ansi"

	"net"
)

var matchid = uint64(0)
var connid = uint64(0)
var localAddr = flag.String("l", ":9999", "local address")
var remoteAddr = flag.String("r", "localhost:80", "remote address")
var verbose = flag.Bool("v", false, "display server actions")
var veryverbose = flag.Bool("vv", false, "display server actions and all tcp data")
var nagles = flag.Bool("n", false, "disable nagles algorithm")
var hex = flag.Bool("h", false, "output hex")
var colors = flag.Bool("c", false, "output ansi colors")
var match = flag.String("match", "", "match regex (in the form 'regex')")
var replace = flag.String("replace", "", "replace regex (in the form 'regex~replacer')")

func main() {
	flag.Parse()
	fmt.Printf(c("Proxying from %v to %v\n", "green"), *localAddr, *remoteAddr)

	laddr, err := net.ResolveTCPAddr("tcp", *localAddr)
	check(err)
	raddr, err := net.ResolveTCPAddr("tcp", *remoteAddr)
	check(err)
	listener, err := net.ListenTCP("tcp", laddr)
	check(err)

	matcher := createMatcher(*match)
	replacer := createReplacer(*replace)

	if *veryverbose {
		*verbose = true
	}

	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			fmt.Printf("Failed to accept connection '%s'\n", err)
			continue
		}
		connid++

		p := &proxy{
			lconn:    conn,
			laddr:    laddr,
			raddr:    raddr,
			erred:    false,
			errsig:   make(chan bool),
			prefix:   fmt.Sprintf("Connection #%03d ", connid),
			matcher:  matcher,
			replacer: replacer,
		}
		go p.start()
	}
}

//A proxy represents a pair of connections and their state
type proxy struct {
	sentBytes     uint64
	receivedBytes uint64
	laddr, raddr  *net.TCPAddr
	lconn, rconn  *net.TCPConn
	erred         bool
	errsig        chan bool
	prefix        string
	matcher       func([]byte)
	replacer      func([]byte) []byte
}

func (p *proxy) log(s string, args ...interface{}) {
	if *verbose {
		log(p.prefix+s, args...)
	}
}

func (p *proxy) err(s string, err error) {
	if p.erred {
		return
	}
	if err != io.EOF {
		warn(p.prefix+s, err)
	}
	p.errsig <- true
	p.erred = true
}

func (p *proxy) start() {
	defer p.lconn.Close()
	//connect to remote
	rconn, err := net.DialTCP("tcp", nil, p.raddr)
	if err != nil {
		p.err("Remote connection failed: %s", err)
		return
	}
	p.rconn = rconn
	defer p.rconn.Close()
	//nagles?
	if *nagles {
		p.lconn.SetNoDelay(true)
		p.rconn.SetNoDelay(true)
	}
	//display both ends
	p.log("Opened %s >>> %s", p.lconn.RemoteAddr().String(), p.rconn.RemoteAddr().String())
	//bidirectional copy
	go p.pipe(p.lconn, p.rconn)
	go p.pipe(p.rconn, p.lconn)
	//wait for close...
	<-p.errsig
	p.log("Closed (%d bytes sent, %d bytes recieved)", p.sentBytes, p.receivedBytes)
}

func (p *proxy) pipe(src, dst *net.TCPConn) {
	//data direction
	var f, h string
	islocal := src == p.lconn
	if islocal {
		f = ">>> %d bytes sent%s"
	} else {
		f = "<<< %d bytes recieved%s"
	}
	//output hex?
	if *hex {
		h = "%x"
	} else {
		h = "%s"
	}
	//directional copy (64k buffer)
	buff := make([]byte, 0xffff)
	for {
		n, err := src.Read(buff)
		if err != nil {
			p.err("Read failed '%s'\n", err)
			return
		}
		b := buff[:n]
		//execute match
		if p.matcher != nil {
			p.matcher(b)
		}
		//execute replace
		if p.replacer != nil {
			b = p.replacer(b)
		}
		//show output
		if *veryverbose {
			p.log(f, n, "\n"+c(fmt.Sprintf(h, b), "blue"))
		} else {
			p.log(f, n, "")
		}
		//write out result
		n, err = dst.Write(b)
		if err != nil {
			p.err("Write failed '%s'\n", err)
			return
		}
		if islocal {
			p.sentBytes += uint64(n)
		} else {
			p.receivedBytes += uint64(n)
		}
	}
}

//helper functions

func check(err error) {
	if err != nil {
		warn(err.Error())
		os.Exit(1)
	}
}

func c(str, style string) string {
	if *colors {
		return ansi.Color(str, style)
	}
	return str
}

func log(f string, args ...interface{}) {
	fmt.Printf(c(f, "green")+"\n", args...)
}

func warn(f string, args ...interface{}) {
	fmt.Printf(c(f, "red")+"\n", args...)
}

func createMatcher(match string) func([]byte) {
	if match == "" {
		return nil
	}
	re, err := regexp.Compile(match)
	if err != nil {
		warn("Invalid match regex: %s", err)
		return nil
	}

	log("Matching %s", re.String())
	return func(input []byte) {
		ms := re.FindAll(input, -1)
		for _, m := range ms {
			matchid++
			log("Match #%d: %s", matchid, string(m))
		}
	}
}

func createReplacer(replace string) func([]byte) []byte {
	if replace == "" {
		return nil
	}
	//split by / (TODO: allow slash escapes)
	parts := strings.Split(replace, "~")
	if len(parts) != 2 {
		fmt.Println(c("Invalid replace option", "red"))
		return nil
	}

	re, err := regexp.Compile(string(parts[0]))
	if err != nil {
		warn("Invalid replace regex: %s", err)
		return nil
	}

	repl := []byte(parts[1])

	log("Replacing %s with %s", re.String(), repl)
	return func(input []byte) []byte {
		return re.ReplaceAll(input, repl)
	}
}
