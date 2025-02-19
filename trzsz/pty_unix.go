//go:build !windows

/*
MIT License

Copyright (c) 2022 Lonny Wong

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

package trzsz

import (
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type TrzszPty struct {
	Stdin  PtyIO
	Stdout PtyIO
	ptmx   *os.File
	cmd    *exec.Cmd
	ch     chan os.Signal
	resize func(int)
	mutex  sync.Mutex
	closed bool
}

func Spawn(name string, arg ...string) (*TrzszPty, error) {
	// spawn a pty
	cmd := exec.Command(name, arg...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	// handle pty size
	ch := make(chan os.Signal, 1)
	tPty := &TrzszPty{Stdin: ptmx, Stdout: ptmx, ptmx: ptmx, cmd: cmd, ch: ch}
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			tPty.Resize()
		}
	}()
	ch <- syscall.SIGWINCH

	return tPty, nil
}

func (t *TrzszPty) OnResize(cb func(int)) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.resize = cb
}

func (t *TrzszPty) Resize() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.closed {
		return nil
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return err
	}
	if err := pty.Setsize(t.ptmx, size); err != nil {
		return err
	}
	if t.resize != nil {
		t.resize(int(size.Cols))
	}
	return nil
}

func (t *TrzszPty) GetColumns() (int, error) {
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return 0, err
	}
	return int(size.Cols), nil
}

func (t *TrzszPty) Close() {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	signal.Stop(t.ch)
	close(t.ch)
	t.ptmx.Close()
}

func (t *TrzszPty) Wait() {
	t.cmd.Wait()
}

func (t *TrzszPty) Terminate() {
	t.cmd.Process.Signal(syscall.SIGTERM)
}

func (t *TrzszPty) ExitCode() int {
	return t.cmd.ProcessState.ExitCode()
}

func syscallAccessWok(path string) error {
	return syscall.Access(path, unix.W_OK)
}

func syscallAccessRok(path string) error {
	return syscall.Access(path, unix.R_OK)
}

func enableVirtualTerminal() (uint32, uint32, error) {
	return 0, 0, nil
}

func resetVirtualTerminal(inMode, outMode uint32) error {
	return nil
}

func setupConsoleOutput() {
}
