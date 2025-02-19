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
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mockTimeNow(times []int64) *int {
	idx := 0
	timeNowFunc = func() time.Time {
		if idx > len(times) {
			return time.UnixMilli(0)
		}
		t := time.UnixMilli(times[idx])
		idx++
		return t
	}
	return &idx
}

var colorRegexp = regexp.MustCompile(`\x1b\[\d+[mD]`)

func getProgressLength(text string) int {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\u2588", "*")
	text = strings.ReplaceAll(text, "\u2591", "*")
	text = colorRegexp.ReplaceAllString(text, "")
	return getDisplayLength(text)
}

type ProgressWriter struct {
	t      *testing.T
	buffer []string
}

func (w *ProgressWriter) Write(text []byte) (n int, err error) {
	w.buffer = append(w.buffer, string(text))
	return len(text), nil
}

func (w *ProgressWriter) assertBufferCount(count int) {
	w.t.Helper()
	require.Equal(w.t, count, len(w.buffer))
}

func (w *ProgressWriter) assertBufferText(idx int, size int, expected []string) {
	w.t.Helper()
	require.Less(w.t, idx, len(w.buffer))
	assert.Equal(w.t, size, getProgressLength(w.buffer[idx]))
	for _, text := range expected {
		assert.Contains(w.t, w.buffer[idx], text)
	}
}

func NewProgressWriter(t *testing.T) *ProgressWriter {
	return &ProgressWriter{t, nil}
}

func TestProgressWithEmptyFile(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564135000})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(0)
	progress.onStep(0)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 100% | 0.00 B | --- B/s | --- ETA"})
}

func TestProgressZeroStep(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564135100})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(100)
	progress.onStep(0)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 0% | 0.00 B | --- B/s | --- ETA"})
}

func TestProgressLastStep(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564135200})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(100)
	progress.onStep(100)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 100% | 100 B | 500 B/s | 00:00 ETA"})
}

func TestProgressWithSpeedAndEta(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564135100})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(100)
	progress.onStep(1)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 1% | 1.00 B | 10.0 B/s | 00:10 ETA"})
}

func TestProgressNewestSpeed(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	now := int64(1646564135000)
	var mockTimes []int64
	for i := 0; i < 101; i++ {
		mockTimes = append(mockTimes, now+int64(i*1000))
	}
	callTimeNowCount := mockTimeNow(mockTimes)

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(100000)
	step := int64(100)
	for i := 0; i < 100; i++ {
		step += int64(i * 10)
		progress.onStep(step)
	}

	assert.Equal(101, *callTimeNowCount)
	writer.assertBufferCount(100)
	total := float64(100)
	for i := 0; i < 100; i++ {
		total += float64(i * 10)
		percentageStr := fmt.Sprintf("%.0f", math.Round(total*100.0/100000.0))
		var speed float64
		if i < 30 {
			speed = total / float64(i+1)
		} else {
			t := 0
			for j := i - 30 + 1; j <= i; j++ {
				t += j * 10
			}
			speed = float64(t) / 30.0
		}
		totalStr := fmt.Sprintf("%.0f B", total)
		if total >= 10240 {
			totalStr = fmt.Sprintf("%.1f KB", total/1024.0)
		} else if total >= 1024 {
			totalStr = fmt.Sprintf("%.2f KB", total/1024.0)
		}
		speedStr := fmt.Sprintf("%.1f", speed)
		if speed >= 100 {
			speedStr = fmt.Sprintf("%.0f", speed)
		}
		eta := math.Round((100000.0 - total) / speed)
		minute := math.Floor(eta / 60)
		second := int64(math.Round(eta)) % 60
		minuteStr := fmt.Sprintf("%.0f", minute)
		if minute < 10 {
			minuteStr = "0" + minuteStr
		}
		secondStr := fmt.Sprintf("%d", second)
		if second < 10 {
			secondStr = "0" + secondStr
		}

		text := fmt.Sprintf("] %s%% | %s | %s B/s | %s:%s ETA", percentageStr, totalStr, speedStr, minuteStr, secondStr)
		writer.assertBufferText(i, 100, []string{"中文😀test.txt [", text})
	}

}

func TestProgressReduceOutput(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564135001, 1646564135099})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(100)
	progress.onStep(1)
	progress.onStep(2)

	assert.Equal(3, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 1% | 1.00 B | 1000 B/s | 00:00 ETA"})
}

func TestProgressFastSpeed(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(1125899906842624)
	progress.onStep(11105067440538)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 1% | 10.1 TB | 10.1 TB/s | 01:40 ETA"})
}

func TestProgressSlowSpeed(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(1024 * 1024)
	progress.onStep(1)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	writer.assertBufferText(0, 100, []string{"中文😀test.txt [", "] 0% | 1.00 B | 1.00 B/s | 291:16:15 ETA"})
}

func TestProgressLongFileName(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564138000})

	progress := NewTextProgressBar(writer, 110, 0)
	progress.onNum(1)
	progress.onName("中文😀非常长非常长非常长非常长非常长非常长非常长非常长.txt")
	progress.onSize(1024 * 1024)
	progress.onStep(100 * 1024)
	progress.setTerminalColumns(100)
	progress.onStep(200 * 1024)

	assert.Equal(3, *callTimeNowCount)
	writer.assertBufferCount(2)
	writer.assertBufferText(0, 110, []string{
		"中文😀非常长非常长非常长非常长非常长非常长非常... [", "] 10% | 100 KB | 100 KB/s | 00:09 ETA"})
	writer.assertBufferText(1, 100, []string{
		"中文😀非常长非常长非常长非常长非常长... [", "] 20% | 200 KB | 66.7 KB/s | 00:12 ETA"})
}

func TestProgressWithoutTotalSize(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564138000})

	progress := NewTextProgressBar(writer, 95, 0)
	progress.onNum(1)
	progress.onName("中文😀非常长非常长非常长非常长非常长非常长非常长非常长.txt")
	progress.onSize(1000 * 1024 * 1024 * 1024)
	progress.onStep(100 * 1024 * 1024)
	progress.setTerminalColumns(85)
	progress.onStep(200 * 1024 * 1024 * 1024)

	assert.Equal(3, *callTimeNowCount)
	writer.assertBufferCount(2)
	writer.assertBufferText(0, 95, []string{"中文😀非常长非常长非常长非常长非常长... [", "] 0% | 100 MB/s | 2:50:39 ETA"})
	writer.assertBufferText(1, 85, []string{"中文😀非常长非常长非常长非... [", "] 20% | 66.7 GB/s | 00:12 ETA"})
}

func TestProgressWithoutSpeedOrEta(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564138000})

	progress := NewTextProgressBar(writer, 70, 0)
	progress.onNum(1)
	progress.onName("中文😀longlonglonglonglonglongname.txt")
	progress.onSize(1000)
	progress.onStep(100)
	progress.setTerminalColumns(60)
	progress.onStep(200)

	assert.Equal(3, *callTimeNowCount)
	writer.assertBufferCount(2)
	writer.assertBufferText(0, 70, []string{"中文😀longlonglonglonglongl... [", "] 10% | 00:09 ETA"})
	writer.assertBufferText(1, 60, []string{"中文😀longlonglonglonglongl... [", "] 20%"})
}

func TestProgressWithoutFileName(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564138000})

	progress := NewTextProgressBar(writer, 48, 0)
	progress.onNum(1)
	progress.onName("中文😀llong文件名.txt")
	progress.onSize(1000)
	progress.onStep(100)
	progress.setTerminalColumns(30)
	progress.onStep(200)

	assert.Equal(3, *callTimeNowCount)
	writer.assertBufferCount(2)
	writer.assertBufferText(0, 48, []string{"中文😀llong文件名... [", "] 10%"})
	writer.assertBufferText(1, 30, []string{"] 20%"})
	assert.NotContains(writer.buffer[1], "中文")
}

func TestProgressWithoutBar(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000})

	progress := NewTextProgressBar(writer, 10, 0)
	progress.onNum(1)
	progress.onName("中文😀test.txt")
	progress.onSize(1000)
	progress.onStep(300)

	assert.Equal(2, *callTimeNowCount)
	writer.assertBufferCount(1)
	assert.Equal("30%", writer.buffer[0])
}

func TestProgressWithMultiFiles(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564137000, 1646564139000})

	progress := NewTextProgressBar(writer, 100, 0)
	progress.onNum(2)
	progress.onName("中文😀test.txt")
	progress.onSize(1000)
	progress.onStep(100)
	progress.onDone()
	progress.onName("英文😀test.txt")
	progress.onSize(2000)
	progress.setTerminalColumns(80)
	progress.onStep(300)
	progress.onDone()

	assert.Equal(4, *callTimeNowCount)
	writer.assertBufferCount(4)
	writer.assertBufferText(0, 100, []string{"(1/2) 中文😀test.txt [", "] 10% | 100 B | 100 B/s | 00:09 ETA"})
	assert.Equal("\r", writer.buffer[1])
	writer.assertBufferText(2, 80, []string{"(2/2) 英文😀test.txt [", "] 15% | 300 B | 150 B/s | 00:11 ETA"})
	assert.Equal("\r", writer.buffer[3])
}

func TestProgressInTmuxPane(t *testing.T) {
	assert := assert.New(t)
	writer := NewProgressWriter(t)
	callTimeNowCount := mockTimeNow([]int64{1646564135000, 1646564136000, 1646564137000, 1646564138000, 1646564139000})

	progress := NewTextProgressBar(writer, 100, 80)
	progress.onNum(2)
	progress.onName("中文😀test.txt")
	progress.onSize(1000)
	progress.onStep(100)
	progress.onStep(200)
	progress.onDone()
	progress.onName("中文😀test2.txt")
	progress.onSize(1000)
	progress.setTerminalColumns(120)
	progress.onStep(300)
	progress.onDone()

	assert.Equal(5, *callTimeNowCount)
	writer.assertBufferCount(5)
	writer.assertBufferText(0, 79, []string{"(1/2) 中文😀test.txt [", "] 10% | 100 B | 100 B/s | 00:09 ETA"})
	assert.NotContains(writer.buffer[0], "\r")
	assert.NotContains(writer.buffer[0], "\x1b[79D")

	writer.assertBufferText(1, 79, []string{"\x1b[79D", "(1/2) 中文😀test.txt [", "] 20% | 200 B | 100 B/s | 00:08 ETA"})
	assert.NotContains(writer.buffer[1], "\r")

	assert.NotContains(writer.buffer[2], "\r")
	assert.Contains(writer.buffer[2], "\x1b[79D")

	writer.assertBufferText(3, 120, []string{"(2/2) 中文😀test2.txt [", "] 30% | 300 B | 300 B/s | 00:02 ETA"})

	assert.Contains(writer.buffer[4], "\r")
	assert.NotContains(writer.buffer[4], "\x1b[79D")
}

func TestProgressEllipsisString(t *testing.T) {
	assert := assert.New(t)
	assertEllipsisEqual := func(str string, max int, expectedStr string, expectedLen int) {
		t.Helper()
		s, l := getEllipsisString(str, max)
		assert.Equal(expectedStr, s)
		assert.Equal(expectedLen, l)
	}
	assertEllipsisEqual("", 10, "...", 3)
	assertEllipsisEqual("中文", 1, "...", 3)
	assertEllipsisEqual("中文", 5, "中...", 5)
	assertEllipsisEqual("😀中", 5, "😀...", 5)
	assertEllipsisEqual("😀中", 6, "😀...", 5)
	assertEllipsisEqual("😀中", 7, "😀中...", 7)
	assertEllipsisEqual("😀q中", 5, "😀...", 5)
	assertEllipsisEqual("😀a中", 6, "😀a...", 6)
	assertEllipsisEqual("😀a中", 7, "😀a...", 6)
	assertEllipsisEqual("😀a中", 8, "😀a中...", 8)
}
