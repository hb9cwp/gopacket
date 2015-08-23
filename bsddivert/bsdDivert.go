// +build openbsd

// 
package bsddivert

import (
        "github.com/google/gopacket"
        "golang.org/x/sys/unix"

        "fmt"
        "syscall"
        "time"
        "unsafe"
)


