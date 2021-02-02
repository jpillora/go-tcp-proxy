package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"io"
	"net"

	"github.com/tanopwan/go-tcp-proxy/amqphelper"
)

// AMQPProxy - Manages a Proxy connection, piping data between local and remote, and support AMQP frame
type AMQPProxy struct {
	sentBytes     uint64
	receivedBytes uint64
	laddr, raddr  *net.TCPAddr
	lconn, rconn  io.ReadWriteCloser
	erred         bool
	errsig        chan bool
	tlsUnwrapp    bool
	tlsAddress    string

	Matcher  func([]byte)
	Replacer func([]byte) []byte

	// Settings
	Nagles    bool
	Log       Logger
	OutputHex bool
}

// NewAMQP - Create a new Proxy instance. Takes over local connection passed in,
// and closes it when finished.
func NewAMQP(lconn *net.TCPConn, laddr, raddr *net.TCPAddr) *AMQPProxy {
	return &AMQPProxy{
		lconn:  lconn,
		laddr:  laddr,
		raddr:  raddr,
		erred:  false,
		errsig: make(chan bool),
		Log:    NullLogger{},
	}
}

// NewAMQPTLSUnwrapped - Create a new Proxy instance with a remote TLS server for
// which we want to unwrap the TLS to be able to connect without encryption
// locally
func NewAMQPTLSUnwrapped(lconn *net.TCPConn, laddr, raddr *net.TCPAddr, addr string) *AMQPProxy {
	p := NewAMQP(lconn, laddr, raddr)
	p.tlsUnwrapp = true
	p.tlsAddress = addr
	return p
}

// Start - open connection to remote and start proxying data.
func (p *AMQPProxy) Start() {
	defer p.lconn.Close()

	var err error
	//connect to remote
	if p.tlsUnwrapp {
		p.rconn, err = tls.Dial("tcp", p.tlsAddress, nil)
	} else {
		p.rconn, err = net.DialTCP("tcp", nil, p.raddr)
	}
	if err != nil {
		p.Log.Warn("Remote connection failed: %s", err)
		return
	}
	defer p.rconn.Close()

	//nagles?
	if p.Nagles {
		if conn, ok := p.lconn.(setNoDelayer); ok {
			conn.SetNoDelay(true)
		}
		if conn, ok := p.rconn.(setNoDelayer); ok {
			conn.SetNoDelay(true)
		}
	}

	//display both ends
	p.Log.Info("Opened %s >>> %s", p.laddr.String(), p.raddr.String())

	//bidirectional copy
	go p.pipe(p.lconn, p.rconn)
	go p.pipe(p.rconn, p.lconn)

	//wait for close...
	<-p.errsig
	p.Log.Info("Closed (%d bytes sent, %d bytes recieved)", p.sentBytes, p.receivedBytes)
}

func (p *AMQPProxy) err(s string, err error) {
	if p.erred {
		return
	}
	if err != io.EOF {
		p.Log.Warn(s, err)
	}
	p.errsig <- true
	p.erred = true
}

func (p *AMQPProxy) pipe(src, dst io.ReadWriter) {
	islocal := src == p.lconn

	var dataDirection string
	if islocal {
		dataDirection = ">>> %d bytes sent%s"
	} else {
		dataDirection = "<<< %d bytes received%s"
	}

	var byteFormat string
	if p.OutputHex {
		byteFormat = "%x"
	} else {
		byteFormat = "%s"
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
		if p.Matcher != nil {
			p.Matcher(b)
		}

		//execute replace
		if p.Replacer != nil {
			b = p.Replacer(b)
		}

		//show output
		p.Log.Debug(dataDirection, n, "")
		p.Log.Trace(byteFormat, b)

		buf := bufio.NewReader(bytes.NewReader(buff))
		frameReader := amqphelper.NewFrameReader(buf)
		err = frameReader.ReadFrame()
		if err == nil {
			p.Log.Debug("found Basic ACK -> replace with NACK")
			frameReader.UpdateMethodAckToNack()
			err = frameReader.Write(dst)
			if err != nil {
				p.Log.Warn(err.Error())
				return
			}

			if islocal {
				p.sentBytes += uint64(n)
			} else {
				p.receivedBytes += uint64(n)
			}
			continue
		}
		if err != nil {
			p.Log.Warn(err.Error())
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
