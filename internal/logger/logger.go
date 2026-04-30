package logger

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Collector collects output from a process and writes it to a log file with rotation.
type Collector struct {
	mu          sync.RWMutex
	path        string
	writer      *lumberjack.Logger
	buf         *bufio.Writer
	closed      bool
	subscribers []chan LogLine
}

// LogLine represents a single log entry.
type LogLine struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
}

// NewCollector creates a new log collector.
// maxSize is a string like "100MB", maxFiles is the number of rotated files to keep.
func NewCollector(path string, maxSize string, maxFiles int) (*Collector, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating log directory %s: %w", dir, err)
	}

	// Parse maxSize
	var maxBytes int64 = 100 * 1024 * 1024 // default 100MB
	if maxSize != "" {
		maxBytes = parseSize(maxSize)
	}
	if maxFiles <= 0 {
		maxFiles = 5
	}

	lj := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    int(maxBytes / (1024 * 1024)),
		MaxBackups: maxFiles,
		MaxAge:     0, // don't use age-based cleanup
		Compress:   false,
		LocalTime:  true,
	}

	return &Collector{
		path:   path,
		writer: lj,
		buf:    bufio.NewWriterSize(lj, 64*1024),
	}, nil
}

// Collect reads from r and writes to the log file. Blocks until r is closed or Collect is closed.
func (c *Collector) Collect(r io.Reader, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Text()
		entry := LogLine{
			Timestamp: time.Now(),
			Stream:    stream,
			Line:      line,
		}

		c.mu.Lock()
		if !c.closed {
			fmt.Fprintf(c.buf, "[%s] %s\n", entry.Timestamp.Format("2006-01-02 15:04:05.000"), line)

			// Notify subscribers
			for _, ch := range c.subscribers {
				select {
				case ch <- entry:
				default:
					// subscriber too slow, drop
				}
			}
		}
		c.mu.Unlock()
	}

	c.mu.Lock()
	if !c.closed {
		c.buf.Flush()
	}
	c.mu.Unlock()
}

// Subscribe returns a channel that receives new log lines.
func (c *Collector) Subscribe(bufferSize int) chan LogLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan LogLine, bufferSize)
	c.subscribers = append(c.subscribers, ch)
	return ch
}

// Unsubscribe removes a subscriber channel.
func (c *Collector) Unsubscribe(ch chan LogLine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, sub := range c.subscribers {
		if sub == ch {
			c.subscribers = append(c.subscribers[:i], c.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// Close flushes and closes the collector.
func (c *Collector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.buf.Flush()
		c.writer.Close()
		for _, ch := range c.subscribers {
			close(ch)
		}
		c.subscribers = nil
	}
}

// Path returns the log file path.
func (c *Collector) Path() string {
	return c.path
}

// Reader provides methods to read log files.
type Reader struct{}

// ReadLastNLines reads the last n lines from the log file.
func (r *Reader) ReadLastNLines(path string, n int) ([]LogLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	// Get file size
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// Read from the end backwards
	var lines []string
	buf := make([]byte, 4096)
	pos := stat.Size()

	for pos > 0 && len(lines) <= n {
		readSize := int64(len(buf))
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		f.Seek(pos, io.SeekStart)
		_, err := io.ReadFull(f, buf[:readSize])
		if err != nil {
			return nil, err
		}

		// Split the buffer and handle the first line specially
		chunk := string(buf[:readSize])
		newLines := strings.Split(chunk, "\n")

		if len(lines) == 0 {
			lines = newLines
		} else {
			// Merge the last line of the new chunk with the first line of existing lines
			newLines[len(newLines)-1] += lines[0]
			lines = append(newLines, lines[1:]...)
		}

		// Limit memory
		if len(lines) > n+10 {
			lines = lines[len(lines)-n-5:]
		}
	}

	// Take the last n lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	var result []LogLine
	for _, line := range lines {
		if line == "" {
			continue
		}
		result = append(result, LogLine{
			Timestamp: time.Now(),
			Line:      line,
		})
	}
	return result, nil
}

// GetSize returns the current size of the log file.
func (r *Reader) GetSize(path string) (int64, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return stat.Size(), nil
}

// parseSize parses a size string like "100MB", "10KB", "1GB" into bytes.
func parseSize(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0
	}

	var multiplier int64 = 1
	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		multiplier = 1
		s = strings.TrimSuffix(s, "B")
	}

	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val * multiplier
}
