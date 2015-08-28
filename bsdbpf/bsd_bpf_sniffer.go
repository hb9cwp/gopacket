// Copyright 2012 Google, Inc. All rights reserved.
//
// Use of this source code is governed by a BSD-style license
// that can be found in the LICENSE file in the root of the source
// tree.

// +build darwin dragonfly freebsd netbsd openbsd

package bsdbpf

import (
	"github.com/google/gopacket"
	"golang.org/x/sys/unix"

	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const wordSize = int(unsafe.Sizeof(uintptr(0)))

func bpfWordAlign(x int) int {
	return (((x) + (wordSize - 1)) &^ (wordSize - 1))
}

// Options is used to configure various properties of the BPF sniffer.
// Default values are used when a nil Options pointer is passed to NewBPFSniffer.
type Options struct {
	// BPFDeviceName is name of the bpf device to use for sniffing
	// the network device. The default value of BPFDeviceName is empty string
	// which causes the first available BPF device file /dev/bpfX to be used.
	BPFDeviceName string
	// ReadBufLen specifies the size of the buffer used to read packets
	// off the wire such that multiple packets are buffered with each read syscall.
	// Note that an individual packet larger than the buffer size is necessarily truncated.
	// A larger buffer should increase performance because fewer read syscalls would be made.
	// If zero is used, the system's default buffer length will be used which depending on the
	// system may default to 4096 bytes which is not big enough to accomodate some link layers
	// such as WLAN (802.11).
	// ReadBufLen defaults to 32767... however typical BSD manual pages for BPF indicate that
	// if the requested buffer size cannot be accommodated, the closest allowable size will be
	// set and returned... hence our GetReadBufLen method.
	ReadBufLen int
	// Timeout is the length of time to wait before timing out on a read request.
	// Timeout defaults to nil which means no timeout is used.
	Timeout *syscall.Timeval
	// Promisc is set to true for promiscuous mode ethernet sniffing.
	// Promisc defaults to true.
	Promisc bool
	// Immediate is set to true to make our read requests return as soon as a packet becomes available.
	// Otherwise, a read will block until either the kernel buffer becomes full or a timeout occurs.
	// The default is true.
	Immediate bool
	// PreserveLinkAddr is set to false if the link level source address should be filled in automatically
	// by the interface output routine. Set to true if the link level source address will be written,
	// as provided, to the wire.
	// The default is true.
	PreserveLinkAddr bool
}

var defaultOptions = Options{
	BPFDeviceName:    "",
	ReadBufLen:       32767,
	Timeout:          nil,
	Promisc:          true,
	Immediate:        true,
	PreserveLinkAddr: true,
}

// BPFSniffer is a struct used to track state of a BSD BPF ethernet sniffer
// such that gopacket's PacketDataSource interface is implemented.
type BPFSniffer struct {
	options           *Options
	sniffDeviceName   string
	fd                int
	readBuffer        []byte
	lastReadLen       int
	readBytesConsumed int
}

// NewBPFSniffer is used to create BSD-only BPF ethernet sniffer
// iface is the network interface device name that you wish to sniff
// options can set to nil in order to utilize default values for everything.
// Each field of Options also have a default setting if left unspecified by
// the user's custome Options struct.
func NewBPFSniffer(iface string, options *Options) (*BPFSniffer, error) {
	var err error
	enable := 1
	sniffer := BPFSniffer{
		sniffDeviceName: iface,
	}
	if options == nil {
		sniffer.options = &defaultOptions
	} else {
		sniffer.options = options
	}

	if sniffer.options.BPFDeviceName == "" {
		sniffer.pickBpfDevice()
	}

	// setup our read buffer
	if sniffer.options.ReadBufLen == 0 {
		sniffer.options.ReadBufLen, err = syscall.BpfBuflen(sniffer.fd)
		if err != nil {
			return nil, err
		}
	} else {
		sniffer.options.ReadBufLen, err = syscall.SetBpfBuflen(sniffer.fd, sniffer.options.ReadBufLen)
		if err != nil {
			return nil, err
		}
	}
	fmt.Printf("ReadBufLen= %v\n", sniffer.options.ReadBufLen)
	sniffer.readBuffer = make([]byte, sniffer.options.ReadBufLen)

	err = syscall.SetBpfInterface(sniffer.fd, sniffer.sniffDeviceName)
	if err != nil {
		return nil, err
	}

	if sniffer.options.Immediate {
		// turn immediate mode on. This makes the snffer non-blocking.
		err = syscall.SetBpfImmediate(sniffer.fd, enable)
		if err != nil {
			return nil, err
		}
	}

	// the above call to syscall.SetBpfImmediate needs to be made
	// before setting a timer otherwise the reads will block for the
	// entire timer duration even if there are packets to return.
	if sniffer.options.Timeout != nil {
		err = syscall.SetBpfTimeout(sniffer.fd, sniffer.options.Timeout)
		if err != nil {
			return nil, err
		}
	}

        filDrop, err:= bpfFilDrop(sniffer.fd)
        if err != nil {
		return nil, err
        }
        fmt.Println("filDrop= ", filDrop)

	// syscall.BIOCSFILDROP: append definition of SetBpfFilDrop() to 
	//  https://github.com/golang/go/blob/master/src/syscall/bpf_bsd.go
	// temporarily use locally defined setBpfFilDrop()
	//err = syscall.SetBpfFilDrop(sniffer.fd, enable)
	err = setBpfFilDrop(sniffer.fd, enable)
	if err != nil {
		return nil, err
	}

        filDrop, err= bpfFilDrop(sniffer.fd)
        if err != nil {
		return nil, err
        }
        fmt.Println("filDrop= ", filDrop)

	if sniffer.options.PreserveLinkAddr {
		// preserves the link level source address...
		// higher level protocol analyzers will not need this
		err = syscall.SetBpfHeadercmpl(sniffer.fd, enable)
		if err != nil {
			return nil, err
		}
	}

	if sniffer.options.Promisc {
		// forces the interface into promiscuous mode
		err = syscall.SetBpfPromisc(sniffer.fd, enable)
		if err != nil {
			return nil, err
		}
	}

        // Flushes the buffer of incoming packets and resets the statistics
        err = syscall.FlushBpf(sniffer.fd)
	if err != nil {
                //log.Fatal("unable to flush filter")
		return nil, err
        }

	return &sniffer, nil
}

// Close is used to close the file-descriptor of the BPF device file.
func (b *BPFSniffer) Close() error {
	return syscall.Close(b.fd)
}

func (b *BPFSniffer) pickBpfDevice() {
	var err error
	for i := 0; i < 99; i++ {
		b.options.BPFDeviceName = fmt.Sprintf("/dev/bpf%d", i)
		b.fd, err = syscall.Open(b.options.BPFDeviceName, syscall.O_RDWR, 0)
		if err == nil {
			break
		}
	}
}

func (b *BPFSniffer) ReadPacketData() ([]byte, gopacket.CaptureInfo, error) {
	var err error
	if b.readBytesConsumed >= b.lastReadLen {
		b.readBytesConsumed = 0
		b.readBuffer = make([]byte, b.options.ReadBufLen)
		for b.lastReadLen= 0; b.lastReadLen ==0; {	// skip empty frames, e.g. EOF returned by OpenBSD
			b.lastReadLen, err = syscall.Read(b.fd, b.readBuffer);
			if err != nil {
				b.lastReadLen = 0
				fmt.Print("e")
				return nil, gopacket.CaptureInfo{}, err
			}
			if b.lastReadLen ==0 {
				fmt.Print(".")
			}
		}
	}
	hdr := (*unix.BpfHdr)(unsafe.Pointer(&b.readBuffer[b.readBytesConsumed]))
	frameStart := b.readBytesConsumed + int(hdr.Hdrlen)
	rawFrame := b.readBuffer[frameStart : frameStart+int(hdr.Caplen)]
	b.readBytesConsumed += bpfWordAlign(int(hdr.Hdrlen) + int(hdr.Caplen))

	captureInfo := gopacket.CaptureInfo{
		// time the packet was captured, if that is known.
		Timestamp:     time.Unix(int64(hdr.Tstamp.Sec), int64(hdr.Tstamp.Usec)*1000),
		// total number of bytes read off of the wire
		//CaptureLength: len(rawFrame),
		CaptureLength:   int(hdr.Caplen),
		// size of the original packet, should be >=CaptureLength
		//Length:        len(rawFrame),
		Length:          int(hdr.Datalen),
	}
	if captureInfo.Length < captureInfo.CaptureLength {
		fmt.Print("<")
		return nil, gopacket.CaptureInfo{}, err
	}
	//fmt.Printf("hdr= %#v\n", hdr)
	// hdr= &unix.BpfHdr{Tstamp:unix.BpfTimeval{Sec:0x55df4eb7, Usec:0x6ccdc}, Caplen:0x36, 
	//  Datalen:0x36, Hdrlen:0x12, Pad_cgo_0:[2]uint8{0x0, 0x0}}

	fmt.Printf("captureInfo= %#v\n", captureInfo)
	// captureInfo= gopacket.CaptureInfo{Timestamp:time.Time{sec:63576294839, nsec:445660000, 
	//  loc:(*time.Location)(0x851bb20)}, CaptureLength:54, Length:54}

	return rawFrame, captureInfo, nil
}

// GetReadBufLen returns the BPF read buffer length
func (b *BPFSniffer) GetReadBufLen() int {
	return b.options.ReadBufLen
}

// SetBpfReadFilterProgram set up BPF read filter program.
func (b *BPFSniffer) SetBpfReadFilterProgram(fp []syscall.BpfInsn) error {
        if err := syscall.SetBpf(b.fd, fp); err != nil {
		//log.Fatal("unable to set filter program")
		return err
        }
	return nil
}

// CompileBpfExpression compiles filter expression to filter program.
func (b *BPFSniffer) CompileBpfExpression(fs string) ([]syscall.BpfInsn, error) {
	panic(fmt.Sprintf("bsdbpf.CompileBpfExpression() not yet implemented"))	// XXX
	return []syscall.BpfInsn{}, nil
}

// SetBpfReadFilter set up BPF read filter program.
func (b *BPFSniffer) SetBpfReadFilter(fs string) error {
	fp, err:= b.CompileBpfExpression(fs)
	if err != nil {
		return err
	}
	err= b.SetBpfReadFilterProgram(fp)
	return err
}


// XXX move stuff below to
//  https://github.com/golang/go/blob/master/src/syscall/bpf_bsd.go

/*
func BpfFilDrop(fd int) (int, error) {
	var f int
	_, _, err := Syscall(SYS_IOCTL, uintptr(fd), BIOCGFILDROP, uintptr(unsafe.Pointer(&f)))
	if err != 0 {
		return 0, Errno(err)
	}
	return &f, nil
}
*/
func bpfFilDrop(fd int) (int, error) {
        var f int
        _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.BIOCGFILDROP, uintptr(unsafe.Pointer(&f)))
        if err != 0 {
                return 0, syscall.Errno(err)
        }
        return f, nil
}


/*
func SetBpfFilDrop(fd, f int) error {
	_, _, err := Syscall(SYS_IOCTL, uintptr(fd), BIOCSFILDROP, uintptr(unsafe.Pointer(&f)))
	if err != 0 {
		return Errno(err)
	}
	return nil
}
*/
func setBpfFilDrop(fd, f int) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.BIOCSFILDROP, uintptr(unsafe.Pointer(&f)))
	if err != 0 {
		return syscall.Errno(err)
	}
	return nil
}
