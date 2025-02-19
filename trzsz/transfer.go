/*
MIT License

Copyright (c) 2023 Lonny Wong

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
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"
)

type TransferAction struct {
	Lang             string `json:"lang"`
	Version          string `json:"version"`
	Confirm          bool   `json:"confirm"`
	Newline          string `json:"newline"`
	Protocol         int    `json:"protocol"`
	SupportBinary    bool   `json:"binary"`
	SupportDirectory bool   `json:"support_dir"`
}

type TransferConfig struct {
	Quiet           bool        `json:"quiet"`
	Binary          bool        `json:"binary"`
	Directory       bool        `json:"directory"`
	Overwrite       bool        `json:"overwrite"`
	Timeout         int         `json:"timeout"`
	Newline         string      `json:"newline"`
	Protocol        int         `json:"protocol"`
	MaxBufSize      int64       `json:"bufsize"`
	EscapeCodes     EscapeArray `json:"escape_chars"`
	TmuxPaneColumns int         `json:"tmux_pane_width"`
	TmuxOutputJunk  bool        `json:"tmux_output_junk"`
}

type TrzszTransfer struct {
	buffer          *TrzszBuffer
	writer          PtyIO
	stopped         bool
	lastInputTime   atomic.Int64
	cleanTimeout    time.Duration
	maxChunkTime    time.Duration
	stdinState      *term.State
	fileNameMap     map[int]string
	remoteIsWindows bool
	flushInTime     bool
	bufferSize      atomic.Int64
	savedSteps      atomic.Int64
	transferConfig  TransferConfig
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func NewTransfer(writer PtyIO, stdinState *term.State, flushInTime bool) *TrzszTransfer {
	t := &TrzszTransfer{
		buffer:       NewTrzszBuffer(),
		writer:       writer,
		cleanTimeout: 100 * time.Millisecond,
		stdinState:   stdinState,
		fileNameMap:  make(map[int]string),
		flushInTime:  flushInTime,
		transferConfig: TransferConfig{
			Timeout:    20,
			Newline:    "\n",
			MaxBufSize: 10 * 1024 * 1024,
		},
	}
	t.bufferSize.Store(1024)
	return t
}

func (t *TrzszTransfer) addReceivedData(buf []byte) {
	if !t.stopped {
		t.buffer.addBuffer(buf)
	}
	t.lastInputTime.Store(time.Now().UnixMilli())
}

func (t *TrzszTransfer) stopTransferringFiles() {
	t.cleanTimeout = maxDuration(t.maxChunkTime*2, 500*time.Millisecond)
	t.stopped = true
	t.buffer.stopBuffer()
}

func (t *TrzszTransfer) cleanInput(timeoutDuration time.Duration) {
	t.stopped = true
	t.buffer.drainBuffer()
	t.lastInputTime.Store(time.Now().UnixMilli())
	for {
		sleepDuration := timeoutDuration - time.Now().Sub(time.UnixMilli(t.lastInputTime.Load()))
		if sleepDuration <= 0 {
			return
		}
		time.Sleep(sleepDuration)
	}
}

func (t *TrzszTransfer) writeAll(buf []byte) error {
	if gTrzszArgs.TraceLog {
		writeTraceLog(buf, "tosvr")
	}
	return writeAll(t.writer, buf)
}

func (t *TrzszTransfer) sendLine(typ string, buf string) error {
	return t.writeAll([]byte(fmt.Sprintf("#%s:%s%s", typ, buf, t.transferConfig.Newline)))
}

func (t *TrzszTransfer) recvLine(expectType string, mayHasJunk bool, timeout <-chan time.Time) ([]byte, error) {
	if t.stopped {
		return nil, newTrzszError("Stopped")
	}

	if IsWindows() || t.remoteIsWindows {
		line, err := t.buffer.readLineOnWindows(timeout)
		if err != nil {
			return nil, err
		}
		idx := bytes.LastIndex(line, []byte("#"+expectType+":"))
		if idx >= 0 {
			line = line[idx:]
		}
		return line, nil
	}

	line, err := t.buffer.readLine(t.transferConfig.TmuxOutputJunk || mayHasJunk, timeout)
	if err != nil {
		return nil, err
	}

	if t.transferConfig.TmuxOutputJunk || mayHasJunk {
		idx := bytes.LastIndex(line, []byte("#"+expectType+":"))
		if idx >= 0 {
			line = line[idx:]
		}
	}

	return line, nil
}

func (t *TrzszTransfer) recvCheck(expectType string, mayHasJunk bool, timeout <-chan time.Time) (string, error) {
	line, err := t.recvLine(expectType, mayHasJunk, timeout)
	if err != nil {
		return "", err
	}

	idx := bytes.IndexByte(line, ':')
	if idx < 1 {
		return "", NewTrzszError(encodeBytes(line), "colon", true)
	}

	typ := string(line[1:idx])
	buf := string(line[idx+1:])
	if typ != expectType {
		return "", NewTrzszError(buf, typ, true)
	}

	return buf, nil
}

func (t *TrzszTransfer) sendInteger(typ string, val int64) error {
	return t.sendLine(typ, strconv.FormatInt(val, 10))
}

func (t *TrzszTransfer) recvInteger(typ string, mayHasJunk bool, timeout <-chan time.Time) (int64, error) {
	buf, err := t.recvCheck(typ, mayHasJunk, timeout)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(buf, 10, 64)
}

func (t *TrzszTransfer) checkInteger(expect int64) error {
	result, err := t.recvInteger("SUCC", false, nil)
	if err != nil {
		return err
	}
	if result != expect {
		return NewTrzszError(fmt.Sprintf("Integer check [%d] <> [%d]", result, expect), "", true)
	}
	return nil
}

func (t *TrzszTransfer) sendString(typ string, str string) error {
	return t.sendLine(typ, encodeString(str))
}

func (t *TrzszTransfer) recvString(typ string, mayHasJunk bool) (string, error) {
	buf, err := t.recvCheck(typ, mayHasJunk, nil)
	if err != nil {
		return "", err
	}
	b, err := decodeString(buf)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (t *TrzszTransfer) checkString(expect string) error {
	result, err := t.recvString("SUCC", false)
	if err != nil {
		return err
	}
	if result != expect {
		return NewTrzszError(fmt.Sprintf("String check [%s] <> [%s]", result, expect), "", true)
	}
	return nil
}

func (t *TrzszTransfer) sendBinary(typ string, buf []byte) error {
	return t.sendLine(typ, encodeBytes(buf))
}

func (t *TrzszTransfer) recvBinary(typ string, mayHasJunk bool, timeout <-chan time.Time) ([]byte, error) {
	buf, err := t.recvCheck(typ, mayHasJunk, timeout)
	if err != nil {
		return nil, err
	}
	return decodeString(buf)
}

func (t *TrzszTransfer) checkBinary(expect []byte) error {
	result, err := t.recvBinary("SUCC", false, nil)
	if err != nil {
		return err
	}
	if bytes.Compare(result, expect) != 0 {
		return NewTrzszError(fmt.Sprintf("Binary check [%v] <> [%v]", result, expect), "", true)
	}
	return nil
}

func (t *TrzszTransfer) sendData(data []byte) error {
	if !t.transferConfig.Binary {
		return t.sendBinary("DATA", data)
	}
	buf := escapeData(data, t.transferConfig.EscapeCodes)
	if err := t.writeAll([]byte(fmt.Sprintf("#DATA:%d\n", len(buf)))); err != nil {
		return err
	}
	return t.writeAll(buf)
}

func (t *TrzszTransfer) getNewTimeout() <-chan time.Time {
	if t.transferConfig.Timeout > 0 {
		return time.NewTimer(time.Duration(t.transferConfig.Timeout) * time.Second).C
	}
	return nil
}

func (t *TrzszTransfer) recvData() ([]byte, error) {
	timeout := t.getNewTimeout()
	if !t.transferConfig.Binary {
		return t.recvBinary("DATA", false, timeout)
	}
	size, err := t.recvInteger("DATA", false, timeout)
	if err != nil {
		return nil, err
	}
	data, err := t.buffer.readBinary(int(size), timeout)
	if err != nil {
		return nil, err
	}
	return unescapeData(data, t.transferConfig.EscapeCodes), nil
}

func (t *TrzszTransfer) sendAction(confirm, remoteIsWindows bool) error {
	action := &TransferAction{
		Lang:             "go",
		Version:          kTrzszVersion,
		Confirm:          confirm,
		Newline:          "\n",
		Protocol:         2,
		SupportBinary:    true,
		SupportDirectory: true,
	}
	if IsWindows() || remoteIsWindows {
		action.Newline = "!\n"
		action.SupportBinary = false
	}
	actStr, err := json.Marshal(action)
	if err != nil {
		return err
	}
	if remoteIsWindows {
		t.remoteIsWindows = true
		t.transferConfig.Newline = "!\n"
	}
	return t.sendString("ACT", string(actStr))
}

func (t *TrzszTransfer) recvAction() (*TransferAction, error) {
	actStr, err := t.recvString("ACT", false)
	if err != nil {
		return nil, err
	}
	action := &TransferAction{
		Newline:       "\n",
		SupportBinary: true,
	}
	if err := json.Unmarshal([]byte(actStr), action); err != nil {
		return nil, err
	}
	t.transferConfig.Newline = action.Newline
	return action, nil
}

func (t *TrzszTransfer) sendConfig(args *Args, action *TransferAction, escapeChars [][]unicode, tmuxMode TmuxMode, tmuxPaneWidth int) error {
	cfgMap := map[string]interface{}{
		"lang": "go",
	}
	if args.Quiet {
		cfgMap["quiet"] = true
	}
	if args.Binary {
		cfgMap["binary"] = true
		cfgMap["escape_chars"] = escapeChars
	}
	if args.Directory {
		cfgMap["directory"] = true
	}
	cfgMap["bufsize"] = args.Bufsize.Size
	cfgMap["timeout"] = args.Timeout
	if args.Overwrite {
		cfgMap["overwrite"] = true
	}
	if tmuxMode == TmuxNormalMode {
		cfgMap["tmux_output_junk"] = true
		cfgMap["tmux_pane_width"] = tmuxPaneWidth
	}
	if action.Protocol > 0 {
		cfgMap["protocol"] = action.Protocol
	}
	cfgStr, err := json.Marshal(cfgMap)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(cfgStr), &t.transferConfig); err != nil {
		return err
	}
	return t.sendString("CFG", string(cfgStr))
}

func (t *TrzszTransfer) recvConfig() (*TransferConfig, error) {
	cfgStr, err := t.recvString("CFG", true)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(cfgStr), &t.transferConfig); err != nil {
		return nil, err
	}
	return &t.transferConfig, nil
}

func (t *TrzszTransfer) clientExit(msg string) error {
	return t.sendString("EXIT", msg)
}

func (t *TrzszTransfer) recvExit() (string, error) {
	return t.recvString("EXIT", false)
}

func (t *TrzszTransfer) serverExit(msg string) {
	t.cleanInput(500 * time.Millisecond)
	if t.stdinState != nil {
		term.Restore(int(os.Stdin.Fd()), t.stdinState)
	}
	if IsWindows() {
		msg = strings.ReplaceAll(msg, "\n", "\r\n")
		os.Stdout.WriteString("\x1b[H\x1b[2J\x1b[?1049l")
	} else {
		os.Stdout.WriteString("\x1b8\x1b[0J")
	}
	os.Stdout.WriteString(msg)
	os.Stdout.WriteString("\r\n")
}

func (t *TrzszTransfer) clientError(err error) {
	t.cleanInput(t.cleanTimeout)

	trace := true
	if e, ok := err.(*TrzszError); ok {
		trace = e.isTraceBack()
		if e.isRemoteExit() || e.isRemoteFail() {
			return
		}
	}

	typ := "fail"
	if trace {
		typ = "FAIL"
	}
	_ = t.sendString(typ, err.Error())
}

func (t *TrzszTransfer) serverError(err error) {
	t.cleanInput(t.cleanTimeout)

	trace := true
	if e, ok := err.(*TrzszError); ok {
		trace = e.isTraceBack()
		if e.isRemoteExit() || e.isRemoteFail() {
			t.serverExit(e.Error())
			return
		}
	}

	typ := "fail"
	if trace {
		typ = "FAIL"
	}
	_ = t.sendString(typ, err.Error())

	t.serverExit(err.Error())
}

func (t *TrzszTransfer) sendFileNum(num int64, progress ProgressCallback) error {
	if err := t.sendInteger("NUM", num); err != nil {
		return err
	}
	if err := t.checkInteger(num); err != nil {
		return err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onNum(num)
	}
	return nil
}

func (t *TrzszTransfer) sendFileName(f *TrzszFile, progress ProgressCallback) (*os.File, string, error) {
	var fileName string
	if t.transferConfig.Directory {
		jsonName, err := json.Marshal(f)
		if err != nil {
			return nil, "", err
		}
		fileName = string(jsonName)
	} else {
		fileName = f.RelPath[0]
	}
	if err := t.sendString("NAME", fileName); err != nil {
		return nil, "", err
	}
	remoteName, err := t.recvString("SUCC", false)
	if err != nil {
		return nil, "", err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onName(f.RelPath[len(f.RelPath)-1])
	}
	if f.IsDir {
		return nil, remoteName, nil
	}
	file, err := os.Open(f.AbsPath)
	if err != nil {
		return nil, "", err
	}
	return file, remoteName, nil
}

func (t *TrzszTransfer) sendFileSize(file *os.File, progress ProgressCallback) (int64, error) {
	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}
	size := stat.Size()
	if err := t.sendInteger("SIZE", size); err != nil {
		return 0, err
	}
	if err := t.checkInteger(size); err != nil {
		return 0, err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onSize(size)
	}
	return size, nil
}

func (t *TrzszTransfer) sendFileData(file *os.File, size int64, progress ProgressCallback) ([]byte, error) {
	step := int64(0)
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onStep(step)
	}
	bufSize := int64(1024)
	buffer := make([]byte, bufSize)
	hasher := md5.New()
	for step < size {
		beginTime := time.Now()
		n, err := file.Read(buffer)
		if err != nil {
			return nil, err
		}
		length := int64(n)
		data := buffer[:n]
		if err := t.sendData(data); err != nil {
			return nil, err
		}
		if _, err := hasher.Write(data); err != nil {
			return nil, err
		}
		if err := t.checkInteger(length); err != nil {
			return nil, err
		}
		step += length
		if progress != nil && !reflect.ValueOf(progress).IsNil() {
			progress.onStep(step)
		}
		chunkTime := time.Now().Sub(beginTime)
		if length == bufSize && chunkTime < 500*time.Millisecond && bufSize < t.transferConfig.MaxBufSize {
			bufSize = minInt64(bufSize*2, t.transferConfig.MaxBufSize)
			buffer = make([]byte, bufSize)
		} else if chunkTime >= 2*time.Second && bufSize > 1024 {
			bufSize = 1024
			buffer = make([]byte, bufSize)
		}
		if chunkTime > t.maxChunkTime {
			t.maxChunkTime = chunkTime
		}
	}
	return hasher.Sum(nil), nil
}

func (t *TrzszTransfer) sendFileMD5(digest []byte, progress ProgressCallback) error {
	if err := t.sendBinary("MD5", digest); err != nil {
		return err
	}
	if err := t.checkBinary(digest); err != nil {
		return err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onDone()
	}
	return nil
}

func (t *TrzszTransfer) sendFiles(files []*TrzszFile, progress ProgressCallback) ([]string, error) {
	if err := t.sendFileNum(int64(len(files)), progress); err != nil {
		return nil, err
	}

	var remoteNames []string
	for _, f := range files {
		file, remoteName, err := t.sendFileName(f, progress)
		if err != nil {
			return nil, err
		}

		if !containsString(remoteNames, remoteName) {
			remoteNames = append(remoteNames, remoteName)
		}

		if file == nil {
			continue
		}

		defer file.Close()

		size, err := t.sendFileSize(file, progress)
		if err != nil {
			return nil, err
		}

		var digest []byte
		if t.transferConfig.Protocol == 2 {
			digest, err = t.sendFileDataV2(file, size, progress)
		} else {
			digest, err = t.sendFileData(file, size, progress)
		}
		if err != nil {
			return nil, err
		}

		if err := t.sendFileMD5(digest, progress); err != nil {
			return nil, err
		}
	}

	return remoteNames, nil
}

func (t *TrzszTransfer) recvFileNum(progress ProgressCallback) (int64, error) {
	num, err := t.recvInteger("NUM", false, nil)
	if err != nil {
		return 0, err
	}
	if err := t.sendInteger("SUCC", num); err != nil {
		return 0, err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onNum(num)
	}
	return num, nil
}

func doCreateFile(path string) (*os.File, error) {
	file, err := os.Create(path)
	if err != nil {
		if e, ok := err.(*fs.PathError); ok {
			if errno, ok := e.Unwrap().(syscall.Errno); ok {
				if (!IsWindows() && errno == 13) || (IsWindows() && errno == 5) {
					return nil, newTrzszError(fmt.Sprintf("No permission to write: %s", path))
				} else if (!IsWindows() && errno == 21) || (IsWindows() && errno == 0x2000002a) {
					return nil, newTrzszError(fmt.Sprintf("Is a directory: %s", path))
				}
			}
		}
		return nil, newTrzszError(fmt.Sprintf("%v", err))
	}
	return file, nil
}

func doCreateDirectory(path string) error {
	stat, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0755)
	} else if err != nil {
		return err
	}
	if !stat.IsDir() {
		return newTrzszError(fmt.Sprintf("Not a directory: %s", path))
	}
	return nil
}

func (t *TrzszTransfer) createFile(path, fileName string) (*os.File, string, error) {
	var localName string
	if t.transferConfig.Overwrite {
		localName = fileName
	} else {
		var err error
		localName, err = getNewName(path, fileName)
		if err != nil {
			return nil, "", err
		}
	}
	file, err := doCreateFile(filepath.Join(path, localName))
	if err != nil {
		return nil, "", err
	}
	return file, localName, nil
}

func (t *TrzszTransfer) createDirOrFile(path, name string) (*os.File, string, string, error) {
	var f TrzszFile
	if err := json.Unmarshal([]byte(name), &f); err != nil {
		return nil, "", "", err
	}
	if len(f.RelPath) < 1 {
		return nil, "", "", newTrzszError(fmt.Sprintf("Invalid name: %s", name))
	}

	fileName := f.RelPath[len(f.RelPath)-1]

	var localName string
	if t.transferConfig.Overwrite {
		localName = f.RelPath[0]
	} else {
		if v, ok := t.fileNameMap[f.PathID]; ok {
			localName = v
		} else {
			var err error
			localName, err = getNewName(path, f.RelPath[0])
			if err != nil {
				return nil, "", "", err
			}
			t.fileNameMap[f.PathID] = localName
		}
	}

	var fullPath string
	if len(f.RelPath) > 1 {
		p := filepath.Join(append([]string{path, localName}, f.RelPath[1:len(f.RelPath)-1]...)...)
		if err := doCreateDirectory(p); err != nil {
			return nil, "", "", err
		}
		fullPath = filepath.Join(p, fileName)
	} else {
		fullPath = filepath.Join(path, localName)
	}

	if f.IsDir {
		if err := doCreateDirectory(fullPath); err != nil {
			return nil, "", "", err
		}
		return nil, localName, fileName, nil
	}

	file, err := doCreateFile(fullPath)
	if err != nil {
		return nil, "", "", err
	}
	return file, localName, fileName, nil
}

func (t *TrzszTransfer) recvFileName(path string, progress ProgressCallback) (*os.File, string, error) {
	fileName, err := t.recvString("NAME", false)
	if err != nil {
		return nil, "", err
	}

	var file *os.File
	var localName string
	if t.transferConfig.Directory {
		file, localName, fileName, err = t.createDirOrFile(path, fileName)
	} else {
		file, localName, err = t.createFile(path, fileName)
	}
	if err != nil {
		return nil, "", err
	}

	if err := t.sendString("SUCC", localName); err != nil {
		return nil, "", err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onName(fileName)
	}

	return file, localName, nil
}

func (t *TrzszTransfer) recvFileSize(progress ProgressCallback) (int64, error) {
	size, err := t.recvInteger("SIZE", false, nil)
	if err != nil {
		return 0, err
	}
	if err := t.sendInteger("SUCC", size); err != nil {
		return 0, err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onSize(size)
	}
	return size, nil
}

func (t *TrzszTransfer) recvFileData(file *os.File, size int64, progress ProgressCallback) ([]byte, error) {
	defer file.Close()
	step := int64(0)
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onStep(step)
	}
	hasher := md5.New()
	for step < size {
		beginTime := time.Now()
		data, err := t.recvData()
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(data); err != nil {
			return nil, err
		}
		length := int64(len(data))
		step += length
		if progress != nil && !reflect.ValueOf(progress).IsNil() {
			progress.onStep(step)
		}
		if err := t.sendInteger("SUCC", length); err != nil {
			return nil, err
		}
		if _, err := hasher.Write(data); err != nil {
			return nil, err
		}
		chunkTime := time.Now().Sub(beginTime)
		if chunkTime > t.maxChunkTime {
			t.maxChunkTime = chunkTime
		}
	}
	return hasher.Sum(nil), nil
}

func (t *TrzszTransfer) recvFileMD5(digest []byte, progress ProgressCallback) error {
	expectDigest, err := t.recvBinary("MD5", false, nil)
	if err != nil {
		return err
	}
	if bytes.Compare(digest, expectDigest) != 0 {
		return newTrzszError("Check MD5 failed")
	}
	if err := t.sendBinary("SUCC", digest); err != nil {
		return err
	}
	if progress != nil && !reflect.ValueOf(progress).IsNil() {
		progress.onDone()
	}
	return nil
}

func (t *TrzszTransfer) recvFiles(path string, progress ProgressCallback) ([]string, error) {
	num, err := t.recvFileNum(progress)
	if err != nil {
		return nil, err
	}

	var localNames []string
	for i := int64(0); i < num; i++ {
		file, localName, err := t.recvFileName(path, progress)
		if err != nil {
			return nil, err
		}

		if !containsString(localNames, localName) {
			localNames = append(localNames, localName)
		}

		if file == nil {
			continue
		}

		defer file.Close()

		size, err := t.recvFileSize(progress)
		if err != nil {
			return nil, err
		}

		var digest []byte
		if t.transferConfig.Protocol == 2 {
			digest, err = t.recvFileDataV2(file, size, progress)
		} else {
			digest, err = t.recvFileData(file, size, progress)
		}
		if err != nil {
			return nil, err
		}

		if err := t.recvFileMD5(digest, progress); err != nil {
			return nil, err
		}
	}

	return localNames, nil
}
