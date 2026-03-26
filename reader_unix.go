//go:build !windows

package vtinput

import (
	"io"
	"os"
	"syscall"
)

func NewReader(in io.Reader) *Reader {
	r := &Reader{
		in:       in,
		buf:      make([]byte, 0, 128),
		dataChan: make(chan byte, 1024),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}

	if err := syscall.Pipe(r.stopPipe[:]); err != nil {
		return r
	}

	var fd int
	isAFile := false
	if f, ok := in.(*os.File); ok {
		fd = int(f.Fd())
		isAFile = true
	}

	go func() {
		defer syscall.Close(r.stopPipe[0])
		tmp := make([]byte, 1024)

		for {
			if isAFile {
				// Advanced logic for real terminals (session resurrection support)
				readFds := &syscall.FdSet{}
				readFds.Bits[fd/64] |= 1 << (uint(fd) % 64)
				readFds.Bits[r.stopPipe[0]/64] |= 1 << (uint(r.stopPipe[0]) % 64)

				_, err := syscall.Select(maxInt(fd, r.stopPipe[0])+1, readFds, nil, nil, nil)
				if err != nil {
					if err == syscall.EINTR { continue }
					r.errChan <- err
					return
				}

				if (readFds.Bits[r.stopPipe[0]/64] & (1 << (uint(r.stopPipe[0]) % 64))) != 0 {
					return
				}

				n, err := syscall.Read(fd, tmp)
				if n > 0 {
					for i := 0; i < n; i++ { r.dataChan <- tmp[i] }
				}
				if err != nil {
					if err == syscall.EAGAIN || err == syscall.EINTR { continue }
					r.errChan <- err
					return
				}
				if n == 0 {
					r.errChan <- io.EOF
					return
				}
			} else {
				// Fallback for tests (pipes, buffers)
				select {
				case <-r.done:
					return
				default:
					n, err := in.Read(tmp)
					if n > 0 {
						for i := 0; i < n; i++ { r.dataChan <- tmp[i] }
					}
					if err != nil {
						r.errChan <- err
						return
					}
				}
			}
		}
	}()

	return r
}

func maxInt(a, b int) int {
	if a > b { return a }
	return b
}

func (r *Reader) platformClose() {
	syscall.Write(r.stopPipe[1], []byte{0})
	syscall.Close(r.stopPipe[1])
}