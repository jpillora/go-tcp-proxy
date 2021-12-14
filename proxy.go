package proxy

import (
	"crypto/tls"
	"io"
	"net"
)

// Proxy - Manages a Proxy connection, piping data between local and remote.
type Proxy struct {
	sentBytes     uint64
	receivedBytes uint64
	laddr, raddr  *net.TCPAddr
	lconn, rconn  io.ReadWriteCloser
	mReqAddr      *net.TCPAddr
	mRespAddr     *net.TCPAddr
	mReqConn      io.ReadWriteCloser
	mRespConn     io.ReadWriteCloser
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

// New - Create a new Proxy instance. Takes over local connection passed in,
// and closes it when finished.
func New(lconn *net.TCPConn, laddr, raddr, mReqAddr, mRespAddr *net.TCPAddr) *Proxy {
	return &Proxy{
		lconn:     lconn,
		laddr:     laddr,
		raddr:     raddr,
		mReqAddr:  mReqAddr,
		mRespAddr: mRespAddr,
		erred:     false,
		errsig:    make(chan bool),
		Log:       NullLogger{},
	}
}

// NewTLSUnwrapped - Create a new Proxy instance with a remote TLS server for
// which we want to unwrap the TLS to be able to connect without encryption
// locally
func NewTLSUnwrapped(lconn *net.TCPConn, laddr, raddr, mReqAddr, mRespAddr *net.TCPAddr, addr string) *Proxy {
	p := New(lconn, laddr, raddr, mReqAddr, mRespAddr)
	p.tlsUnwrapp = true
	p.tlsAddress = addr
	return p
}

type setNoDelayer interface {
	SetNoDelay(bool) error
}

// Start - open connection to remote and start proxying data.
func (p *Proxy) Start() {
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

	if p.mReqAddr != nil {
		p.mReqConn, err = net.DialTCP("tcp", nil, p.mReqAddr)
		if err != nil {
			p.Log.Warn("Mirror requests connection failed: %s", err)
			return
		}
		defer p.mReqConn.Close()
	}
	if p.mRespAddr != nil {
		p.mRespConn, err = net.DialTCP("tcp", nil, p.mRespAddr)
		if err != nil {
			p.Log.Warn("Mirror responses connection failed: %s", err)
			return
		}
		defer p.mRespConn.Close()
	}

	//nagles?
	if p.Nagles {
		if conn, ok := p.lconn.(setNoDelayer); ok {
			conn.SetNoDelay(true)
		}
		if conn, ok := p.rconn.(setNoDelayer); ok {
			conn.SetNoDelay(true)
		}
		if p.mReqConn != nil {
			if conn, ok := p.mReqConn.(setNoDelayer); ok {
				conn.SetNoDelay(true)
			}
		}
		if p.mRespConn != nil {
			if conn, ok := p.mRespConn.(setNoDelayer); ok {
				conn.SetNoDelay(true)
			}
		}
	}

	//display both ends
	p.Log.Info("Opened %s >>> %s", p.laddr.String(), p.raddr.String())

	//bidirectional copy
	go p.pipe(p.lconn, p.rconn, p.mReqConn)
	go p.pipe(p.rconn, p.lconn, p.mRespConn)

	//wait for close...
	<-p.errsig
	p.Log.Info("Closed (%d bytes sent, %d bytes received)", p.sentBytes, p.receivedBytes)
}

func (p *Proxy) err(s string, err error) {
	if p.erred {
		return
	}
	if err != io.EOF {
		p.Log.Warn(s, err)
	}
	p.errsig <- true
	p.erred = true
}

func (p *Proxy) pipe(src, dst, mirror io.ReadWriter) {
	islocal := src == p.lconn

	var dataDirection string
	if islocal {
		dataDirection = ">>> %d bytes sent %s"
	} else {
		dataDirection = "<<< %d bytes received %s"
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

		//write out result
		n, err = dst.Write(b)
		if err != nil {
			p.err("Write failed '%s'\n", err)
			return
		}
		if mirror != nil {
			_, err = mirror.Write(b)
			if err != nil {
				p.Log.Warn("Write to mirror failed '%s'\n", err)
			}
		}
		if islocal {
			p.sentBytes += uint64(n)
		} else {
			p.receivedBytes += uint64(n)
		}
	}
}
